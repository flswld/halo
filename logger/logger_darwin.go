//go:build darwin
// +build darwin

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

func (l *Logger) getThreadId() (threadId string) {
	return strconv.FormatUint(uint64(C.thread_id()), 10)
}
