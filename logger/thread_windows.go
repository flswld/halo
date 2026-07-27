//go:build windows
// +build windows

package logger

import (
	"strconv"

	"golang.org/x/sys/windows"
)

// getThreadId 获取当前 Windows 线程 ID
func (l *Logger) getThreadId() (threadId string) {
	tid := windows.GetCurrentThreadId()
	threadId = strconv.Itoa(int(tid))
	return threadId
}
