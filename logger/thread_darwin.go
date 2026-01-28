//go:build darwin && !cgo
// +build darwin,!cgo

package logger

func (l *Logger) getThreadId() (threadId string) {
	return "N/A"
}
