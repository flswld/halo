package cpu

/*
#define _GNU_SOURCE

#include <sched.h>
#include <pthread.h>

int bind_cpu_core(int core) {
    cpu_set_t mask;
    CPU_ZERO(&mask);
    CPU_SET(core, &mask);
    int ret = pthread_setaffinity_np(pthread_self(), sizeof(mask), &mask);
    return ret;
}

#undef _GNU_SOURCE
*/
import "C"
import (
	"runtime"
	"sync/atomic"
)

// BindCpuCore 协程绑核
func BindCpuCore(core int) bool {
	runtime.LockOSThread()
	ret := C.bind_cpu_core(C.int(core))
	return ret == 0
}

type SpinLock uint32

func (l *SpinLock) Lock() {
	for {
		ok := atomic.CompareAndSwapUint32((*uint32)(l), 0, 1)
		if ok {
			break
		}
	}
}

func (l *SpinLock) UnLock() {
	atomic.StoreUint32((*uint32)(l), 0)
}
