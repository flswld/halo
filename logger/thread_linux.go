//go:build linux
// +build linux

package logger

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// getThreadId 获取当前 Linux 线程 ID
func (l *Logger) getThreadId() (threadId string) {
	tid := unix.Gettid()
	threadId = strconv.Itoa(tid)
	return threadId
}
