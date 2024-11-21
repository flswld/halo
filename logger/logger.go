package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DEBUG = iota
	INFO
	WARN
	ERROR
)

func ParseLevel(level string) int {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return DEBUG
	case "INFO":
		return INFO
	case "WARN":
		return WARN
	case "ERROR":
		return ERROR
	default:
		return DEBUG
	}
}

var levelMap = map[int][]byte{
	DEBUG: []byte("DEBUG"),
	INFO:  []byte("INFO"),
	WARN:  []byte("WARN"),
	ERROR: []byte("ERROR"),
}

var (
	leftBracket  = []byte("[")
	rightBracket = []byte("]")
	space        = []byte(" ")
	colon        = []byte(":")
	funcBracket  = []byte("()")
	lineFeed     = []byte("\n")
)

var (
	red     = []byte{27, 91, 51, 49, 109}
	green   = []byte{27, 91, 51, 50, 109}
	yellow  = []byte{27, 91, 51, 51, 109}
	blue    = []byte{27, 91, 51, 52, 109}
	magenta = []byte{27, 91, 51, 53, 109}
	cyan    = []byte{27, 91, 51, 54, 109}
	white   = []byte{27, 91, 51, 55, 109}
	reset   = []byte{27, 91, 48, 109}
)

const (
	defaultFileMaxSize = 10485760
	logInfoChanSize    = 1000
	maxWriteCacheNum   = 1000
)

var (
	logger *Logger = nil
	config *Config = nil
)

func GetConfig() *Config {
	return config
}

type Config struct {
	AppName      string
	Level        int
	TrackLine    bool
	TrackThread  bool
	EnableFile   bool
	FileMaxSize  int32
	DisableColor bool
	EnableJson   bool
}

type Logger struct {
	FileTagMap    map[string]*os.File
	LogInfoChan   chan *LogInfo
	WriteBuf      []byte
	WriteCacheNum int32
	CloseChan     chan struct{}
}

type LogInfo struct {
	Time        time.Time
	Level       int
	Msg         *[]byte
	FileName    string
	FuncName    string
	Line        int
	GoroutineId string
	ThreadId    string
	TrackLine   bool
	TrackThread bool
	Tag         string
}

func InitLogger(cfg *Config) {
	if cfg == nil {
		cfg = &Config{
			AppName:      "application",
			Level:        DEBUG,
			TrackLine:    true,
			TrackThread:  false,
			EnableFile:   false,
			FileMaxSize:  0,
			DisableColor: false,
			EnableJson:   false,
		}
	}
	config = cfg
	if config.FileMaxSize == 0 {
		config.FileMaxSize = defaultFileMaxSize
	}

	logger = new(Logger)
	logger.FileTagMap = make(map[string]*os.File)
	logger.LogInfoChan = make(chan *LogInfo, logInfoChanSize)
	logger.WriteBuf = make([]byte, 0)
	logger.WriteCacheNum = 0
	logger.CloseChan = make(chan struct{})
	go logger.doLog()
}

func CloseLogger() {
	logger.CloseChan <- struct{}{}
	<-logger.CloseChan
}

func (l *Logger) doLog() {
	var logBuf bytes.Buffer
	timeBuf := make([]byte, 0, 64)
	exit := false
	exitCountDown := 0
	for {
		select {
		case <-l.CloseChan:
			exit = true
			exitCountDown = len(l.LogInfoChan)
		case logInfo := <-l.LogInfoChan:
			if !config.DisableColor {
				logBuf.Write(cyan)
			}
			logBuf.Write(leftBracket)
			logBuf.Write(logInfo.Time.AppendFormat(timeBuf, "2006-01-02 15:04:05.000"))
			logBuf.Write(rightBracket)
			if !config.DisableColor {
				logBuf.Write(reset)
			}
			logBuf.Write(space)

			if !config.DisableColor {
				switch logInfo.Level {
				case DEBUG:
					logBuf.Write(blue)
				case INFO:
					logBuf.Write(green)
				case WARN:
					logBuf.Write(yellow)
				case ERROR:
					logBuf.Write(red)
				}
			}
			logBuf.Write(leftBracket)
			logBuf.Write(levelMap[logInfo.Level])
			logBuf.Write(rightBracket)
			if !config.DisableColor {
				logBuf.Write(reset)
			}
			logBuf.Write(space)

			if !config.DisableColor && logInfo.Level == ERROR {
				logBuf.Write(red)
				logBuf.Write(*logInfo.Msg)
				logBuf.Write(reset)
			} else {
				logBuf.Write(*logInfo.Msg)
			}

			if logInfo.TrackLine {
				logBuf.Write(space)
				if !config.DisableColor {
					logBuf.Write(magenta)
				}
				logBuf.Write(leftBracket)
				logBuf.Write([]byte(logInfo.FileName))
				logBuf.Write(colon)
				logBuf.Write([]byte(strconv.Itoa(logInfo.Line)))
				logBuf.Write(space)
				logBuf.Write([]byte(logInfo.FuncName))
				logBuf.Write(funcBracket)
				if logInfo.TrackThread {
					logBuf.Write(space)
					logBuf.Write([]byte("goroutine"))
					logBuf.Write(colon)
					logBuf.Write([]byte(logInfo.GoroutineId))
					logBuf.Write(space)
					logBuf.Write([]byte("thread"))
					logBuf.Write(colon)
					logBuf.Write([]byte(logInfo.ThreadId))
				}
				logBuf.Write(rightBracket)
				if !config.DisableColor {
					logBuf.Write(reset)
				}
			}

			logBuf.Write(lineFeed)

			logData := logBuf.Bytes()
			l.writeLog(logData, logInfo.Tag)
			putBuf(logInfo.Msg)
			logInfoPool.Put(logInfo)
			logBuf.Reset()
			timeBuf = timeBuf[0:0]
			if exit {
				exitCountDown--
			}
		}
		if exit && exitCountDown == 0 {
			logger.CloseChan <- struct{}{}
			return
		}
	}
}

func (l *Logger) writeLog(logData []byte, logTag string) {
	if config.EnableFile && logTag != "" {
		l.writeLogFile(logData, logTag)
	}
	l.WriteBuf = append(l.WriteBuf, logData...)
	l.WriteCacheNum++
	if len(l.LogInfoChan) != 0 && l.WriteCacheNum < maxWriteCacheNum {
		return
	}
	l.writeLogConsole(l.WriteBuf)
	if config.EnableFile {
		l.writeLogFile(l.WriteBuf, "")
	}
	l.WriteBuf = l.WriteBuf[0:0]
	l.WriteCacheNum = 0
}

func (l *Logger) writeLogConsole(logData []byte) {
	_, _ = os.Stderr.Write(logData)
}

func (l *Logger) writeLogFile(logData []byte, logTag string) {
	logFile := l.FileTagMap[logTag]
	if logFile == nil {
		fileName := "./log/" + config.AppName + ".log"
		if logTag != "" {
			fileName += "." + logTag
		}
		file, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"open new log file error: %v\n"+string(reset), err))
			return
		}
		logFile = file
		l.FileTagMap[logTag] = logFile
	}
	fileStat, err := logFile.Stat()
	if err != nil {
		_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"get log file stat error: %v\n"+string(reset), err))
		return
	}
	if fileStat.Size() >= int64(config.FileMaxSize) {
		err := logFile.Close()
		if err != nil {
			_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"close old log file error: %v\n"+string(reset), err))
			return
		}
		timeStr := time.Now().Format("20060102150405")
		err = os.Rename(logFile.Name(), logFile.Name()+"."+timeStr)
		if err != nil {
			_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"rename old log file error: %v\n"+string(reset), err))
			return
		}
		fileName := "./log/" + config.AppName + ".log"
		if logTag != "" {
			fileName += "." + logTag
		}
		file, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"open new log file error: %v\n"+string(reset), err))
			return
		}
		logFile = file
		l.FileTagMap[logTag] = logFile
	}
	_, err = logFile.Write(logData)
	if err != nil {
		_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"write log file error: %v\n"+string(reset), err))
		return
	}
}

var bufPool = sync.Pool{New: func() any { return new([]byte) }}

func getBuf() *[]byte {
	p := bufPool.Get().(*[]byte)
	*p = (*p)[0:0]
	return p
}

func putBuf(p *[]byte) {
	if cap(*p) > 64<<10 {
		*p = nil
	}
	bufPool.Put(p)
}

var logInfoPool = sync.Pool{New: func() any { return new(LogInfo) }}

func formatLog(level int, msg string, param []any) {
	newMsg, logFlag := parseLogFlag(msg)
	logInfo := logInfoPool.Get().(*LogInfo)
	logInfo.Time = time.Now()
	logInfo.Level = level
	buf := getBuf()
	if config.EnableJson || logFlag.LogJson == "true" {
		jsonList := make([]any, 0)
		for _, obj := range param {
			data, _ := json.Marshal(obj)
			jsonList = append(jsonList, string(data))
		}
		param = jsonList
	}
	*buf = fmt.Appendf(*buf, newMsg, param...)
	logInfo.Msg = buf
	if config.TrackLine || logFlag.LogLine == "true" {
		logInfo.FileName, logInfo.Line, logInfo.FuncName = logger.getLineFunc()
		logInfo.TrackLine = true
	}
	if config.TrackThread || logFlag.LogThread == "true" {
		logInfo.GoroutineId = logger.getGoroutineId()
		logInfo.ThreadId = logger.getThreadId()
		logInfo.TrackThread = true
	}
	logInfo.Tag = logFlag.LogTag
	logger.LogInfoChan <- logInfo
}

type LogFlag struct {
	LogTag    string
	LogJson   string
	LogLine   string
	LogThread string
}

func parseLogFlag(msg string) (string, LogFlag) {
	logFlag := new(LogFlag)
	logFlagRef := reflect.ValueOf(logFlag).Elem()
	if len(msg) == 0 || msg[0] != '@' {
		return msg, LogFlag{}
	}
	end := 0
	for i := 0; i < len(msg); i++ {
		if msg[i] == '|' {
			end = i
			break
		}
		if msg[i] == '@' {
			cus := 0
			ok := false
			for j := i + 1; j < len(msg); j++ {
				if msg[j] == '(' {
					for k := j + 1; k < len(msg); k++ {
						if msg[k] == ')' {
							name := msg[i+1 : j]
							value := msg[j+1 : k]
							field := logFlagRef.FieldByName(name)
							if !field.IsValid() {
								break
							}
							field.SetString(value)
							ok = true
							cus = k
							break
						}
					}
					if ok {
						break
					} else {
						return msg, LogFlag{}
					}
				}
			}
			if ok {
				i = cus
			} else {
				return msg, LogFlag{}
			}
		}
	}
	if end == 0 {
		return msg, LogFlag{}
	}
	return msg[end+1:], *logFlag
}

func Debug(msg string, param ...any) {
	if config.Level > DEBUG {
		return
	}
	formatLog(DEBUG, msg, param)
}

func Info(msg string, param ...any) {
	if config.Level > INFO {
		return
	}
	formatLog(INFO, msg, param)
}

func Warn(msg string, param ...any) {
	if config.Level > WARN {
		return
	}
	formatLog(WARN, msg, param)
}

func Error(msg string, param ...any) {
	if config.Level > ERROR {
		return
	}
	formatLog(ERROR, msg, param)
}

func (l *Logger) getGoroutineId() (goroutineId string) {
	buf := make([]byte, 32)
	runtime.Stack(buf, false)
	buf = bytes.TrimPrefix(buf, []byte("goroutine "))
	buf = buf[:bytes.IndexByte(buf, ' ')]
	goroutineId = string(buf)
	return goroutineId
}

func (l *Logger) getLineFunc() (fileName string, line int, funcName string) {
	var pc uintptr
	var file string
	var ok bool
	pc, file, line, ok = runtime.Caller(2)
	if !ok {
		return "???", -1, "???"
	}
	fileName = path.Base(file)
	funcName = runtime.FuncForPC(pc).Name()
	split := strings.Split(funcName, ".")
	if len(split) != 0 {
		funcName = split[len(split)-1]
	}
	return fileName, line, funcName
}

func Stack() string {
	buf := make([]byte, 1024)
	for {
		n := runtime.Stack(buf, false)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

func StackAll() string {
	buf := make([]byte, 1024*16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}
