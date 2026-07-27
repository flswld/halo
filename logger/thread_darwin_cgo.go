//go:build darwin && cgo
// +build darwin,cgo

package logger

import (
	"strconv"
)

/*
#include <pthread.h>

static unsigned long long thread_id() {
	unsigned long long tid;
 	pthread_threadid_np(NULL, &tid);
	return tid;
}
*/
import "C"

// getThreadId 获取当前 Darwin 线程 ID
func (l *Logger) getThreadId() (threadId string) {
	return strconv.FormatUint(uint64(C.thread_id()), 10)
}
