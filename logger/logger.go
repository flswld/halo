package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// ParseLevel 将日志级别名称转换为级别值
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
	INFO:  []byte("INFO "),
	WARN:  []byte("WARN "),
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

// GetLogger 返回全局日志器
func GetLogger() *Logger {
	return logger
}

// GetConfig 返回全局日志配置
func GetConfig() *Config {
	return config
}

// Config 定义日志器配置
type Config struct {
	AppName      string // 应用名称
	Level        int    // 最低输出级别
	TrackLine    bool   // 是否记录调用位置
	TrackThread  bool   // 是否记录协程和线程信息
	EnableFile   bool   // 是否写入日志文件
	FilePath     string // 日志目录
	FileSizeCut  bool   // 是否按文件大小切分
	FileMaxSize  int32  // 单个日志文件最大字节数
	FileTimeCut  bool   // 是否按日期切分
	DisableColor bool   // 是否禁用终端颜色
	EnableJson   bool   // 是否将格式化参数编码为 JSON
	FullPath     bool   // 是否记录完整源文件路径
}

// Logger 保存异步日志器的运行状态
type Logger struct {
	FileTagMap      map[string]*os.File // 日志标签对应的文件
	LastLogTime     time.Time           // 上一条日志时间
	LogInfoChan     chan *LogInfo       // 待写日志队列
	WriteBuf        []byte              // 批量写入缓冲区
	WriteCacheNum   int32               // 缓冲的日志条数
	CloseChan       chan struct{}       // 关闭握手管道
	RemoteLogWriter io.Writer           // 远程日志输出器
}

// LogInfo 保存一条待输出日志的元数据
type LogInfo struct {
	Time        time.Time // 日志时间
	Level       int       // 日志级别
	Msg         *[]byte   // 日志内容
	Raw         bool      // 是否直接输出原始内容
	FileName    string    // 调用源文件名
	FuncName    string    // 调用函数名
	Line        int       // 调用行号
	GoroutineId string    // 协程 ID
	ThreadId    string    // 线程 ID
	TrackLine   bool      // 是否输出调用位置
	TrackThread bool      // 是否输出协程和线程信息
	Tag         string    // 日志文件标签
}

// InitLogger 初始化全局异步日志器
func InitLogger(cfg *Config) {
	if cfg == nil {
		cfg = &Config{
			AppName:      "application",
			Level:        DEBUG,
			TrackLine:    true,
			TrackThread:  false,
			EnableFile:   false,
			FilePath:     "./log",
			FileSizeCut:  false,
			FileMaxSize:  defaultFileMaxSize,
			FileTimeCut:  true,
			DisableColor: false,
			EnableJson:   false,
			FullPath:     false,
		}
	}
	config = cfg
	if config.EnableFile {
		if config.FilePath == "" {
			config.FilePath = "./log"
		}
		if config.FileMaxSize == 0 {
			config.FileMaxSize = defaultFileMaxSize
		}
		err := os.MkdirAll(config.FilePath, 0644)
		if err != nil {
			panic(fmt.Sprintf("make log dir error: %v", err))
		}
	}

	logger = new(Logger)
	logger.FileTagMap = make(map[string]*os.File)
	logger.LastLogTime = time.Now()
	logger.LogInfoChan = make(chan *LogInfo, logInfoChanSize)
	logger.WriteBuf = make([]byte, 0)
	logger.WriteCacheNum = 0
	logger.CloseChan = make(chan struct{})
	logger.RemoteLogWriter = nil
	go logger.doLog()
}

// CloseLogger 排空日志队列并关闭全局日志器
func CloseLogger() {
	logger.CloseChan <- struct{}{}
	<-logger.CloseChan
}

// doLog 消费日志队列并完成格式化与写入
func (l *Logger) doLog() {
	var logBuf bytes.Buffer
	timeBuf := make([]byte, 0, 64)
	exit := false
	exitCountDown := 0
	for {
		select {
		case <-l.CloseChan:
			// 关闭时记录队列剩余数量并继续消费 直到已提交日志全部写出
			exit = true
			exitCountDown = len(l.LogInfoChan)
		case logInfo := <-l.LogInfoChan:
			var logData []byte = nil
			if !logInfo.Raw {
				// 普通日志在后台统一拼接时间 级别 调用位置和线程信息
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
					logBuf.Write(space)
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
					logBuf.Write(space)
					logBuf.Write(rightBracket)
					if !config.DisableColor {
						logBuf.Write(reset)
					}
				}

				logBuf.Write(lineFeed)
				logData = logBuf.Bytes()
			} else {
				logData = *logInfo.Msg
			}
			l.writeLog(logData, logInfo.Tag, logInfo.Time)
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

// writeLog 将日志写入启用的输出目标
func (l *Logger) writeLog(logData []byte, logTag string, logTime time.Time) {
	defer func() {
		l.LastLogTime = logTime
	}()
	if config.EnableFile {
		if config.FileTimeCut {
			if l.LastLogTime.Year() != logTime.Year() || l.LastLogTime.YearDay() != logTime.YearDay() {
				for k := range l.FileTagMap {
					l.cutLogFile(k)
				}
			}
		}
		if logTag != "" {
			l.writeLogFile(logData, logTag)
		}
	}
	if l.RemoteLogWriter != nil {
		_, _ = l.RemoteLogWriter.Write(logData)
	}
	l.WriteBuf = append(l.WriteBuf, logData...)
	l.WriteCacheNum++
	// 队列仍有积压且批次未满时延迟系统调用
	if len(l.LogInfoChan) != 0 && l.WriteCacheNum < maxWriteCacheNum {
		return
	}
	l.flushLog()
}

// flushLog 将批量日志缓冲区刷新到控制台和文件
func (l *Logger) flushLog() {
	l.writeLogConsole(l.WriteBuf)
	if config.EnableFile {
		l.writeLogFile(l.WriteBuf, "")
	}
	l.WriteBuf = l.WriteBuf[0:0]
	l.WriteCacheNum = 0
}

// writeLogConsole 将日志写入标准错误
func (l *Logger) writeLogConsole(logData []byte) {
	_, _ = os.Stderr.Write(logData)
}

// writeLogFile 将日志写入指定标签对应的文件
func (l *Logger) writeLogFile(logData []byte, logTag string) {
	logFile := l.FileTagMap[logTag]
	if logFile == nil {
		fileName := config.FilePath + "/" + config.AppName + ".log"
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
	if config.FileSizeCut {
		fileStat, err := logFile.Stat()
		if err != nil {
			_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"get log file stat error: %v\n"+string(reset), err))
			return
		}
		if fileStat.Size() >= int64(config.FileMaxSize) {
			l.cutLogFile(logTag)
			logFile = l.FileTagMap[logTag]
			if logFile == nil {
				return
			}
		}
	}
	_, err := logFile.Write(logData)
	if err != nil {
		_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"write log file error: %v\n"+string(reset), err))
		return
	}
}

// cutLogFile 归档并重建指定标签的日志文件
func (l *Logger) cutLogFile(logTag string) {
	logFile := l.FileTagMap[logTag]
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
	fileName := config.FilePath + "/" + config.AppName + ".log"
	if logTag != "" {
		fileName += "." + logTag
	}
	file, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		_, _ = os.Stderr.WriteString(fmt.Sprintf(string(red)+"open new log file error: %v\n"+string(reset), err))
		return
	}
	l.FileTagMap[logTag] = file
}

var bufPool = sync.Pool{New: func() any { return new([]byte) }}

// getBuf 从缓冲池获取空字节切片
func getBuf() *[]byte {
	p := bufPool.Get().(*[]byte)
	*p = (*p)[0:0]
	return p
}

// putBuf 将字节切片归还缓冲池
func putBuf(p *[]byte) {
	if cap(*p) > 64<<10 {
		*p = nil
	}
	bufPool.Put(p)
}

var logInfoPool = sync.Pool{New: func() any { return new(LogInfo) }}

// formatLog 解析日志标志并构建待输出日志
func formatLog(level int, msg string, param []any) {
	// 消息前缀可覆盖单条日志的标签 JSON 行号和线程输出策略
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
	logInfo.Raw = false
	if config.TrackLine || logFlag.LogLine == "true" {
		logInfo.FileName, logInfo.Line, logInfo.FuncName = logger.getLineFunc(config.FullPath)
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

// LogFlag 保存日志消息内嵌的输出控制标志
type LogFlag struct {
	LogTag    string // 日志文件标签
	LogJson   string // 是否使用 JSON 编码参数
	LogLine   string // 是否记录调用位置
	LogThread string // 是否记录协程和线程信息
}

// parseLogFlag 解析日志消息开头的输出控制标志
func parseLogFlag(msg string) (string, LogFlag) {
	if len(msg) == 0 || msg[0] != '@' {
		return msg, LogFlag{}
	}
	logFlag := new(LogFlag)
	logFlagRef := reflect.ValueOf(logFlag).Elem()
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

// Debug 输出调试级别日志
func Debug(msg string, param ...any) {
	if config.Level > DEBUG {
		return
	}
	formatLog(DEBUG, msg, param)
}

// Info 输出信息级别日志
func Info(msg string, param ...any) {
	if config.Level > INFO {
		return
	}
	formatLog(INFO, msg, param)
}

// Warn 输出警告级别日志
func Warn(msg string, param ...any) {
	if config.Level > WARN {
		return
	}
	formatLog(WARN, msg, param)
}

// Error 输出错误级别日志
func Error(msg string, param ...any) {
	if config.Level > ERROR {
		return
	}
	formatLog(ERROR, msg, param)
}

// Print 以调试级别输出参数列表
func Print(param ...any) {
	msg := make([]byte, 0, 32)
	for i := 0; i < len(param); i++ {
		if i > 0 {
			msg = append(msg, space...)
		}
		msg = append(msg, '%', 'v')
	}
	formatLog(DEBUG, string(msg), param)
}

// Raw 直接输出未经格式化的日志数据
func Raw(data []byte) {
	logInfo := logInfoPool.Get().(*LogInfo)
	logInfo.Time = time.Now()
	buf := getBuf()
	*buf = append(*buf, data...)
	logInfo.Msg = buf
	logInfo.Raw = true
	logger.LogInfoChan <- logInfo
}

// LogWriter 将 io.Writer 写入转换为原始日志
type LogWriter struct{}

// Write 将数据作为原始日志写入全局日志器
func (l *LogWriter) Write(p []byte) (n int, err error) {
	Raw(p)
	return len(p), nil
}

// getGoroutineId 获取当前协程 ID
func (l *Logger) getGoroutineId() (goroutineId string) {
	buf := make([]byte, 32)
	runtime.Stack(buf, false)
	buf = bytes.TrimPrefix(buf, []byte("goroutine "))
	buf = buf[:bytes.IndexByte(buf, ' ')]
	goroutineId = string(buf)
	return goroutineId
}

// getLineFunc 获取日志调用位置和函数名
func (l *Logger) getLineFunc(fullPath bool) (fileName string, line int, funcName string) {
	var pc uintptr
	var file string
	var ok bool
	pc, file, line, ok = runtime.Caller(3)
	if !ok {
		return "???", -1, "???"
	}
	if fullPath {
		fileName = file
	} else {
		fileName = path.Base(file)
	}
	funcName = runtime.FuncForPC(pc).Name()
	split := strings.Split(funcName, "/")
	if len(split) != 0 {
		funcName = split[len(split)-1]
	}
	return fileName, line, funcName
}

// Stack 返回当前协程的调用栈
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

// StackAll 返回全部协程的调用栈
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
