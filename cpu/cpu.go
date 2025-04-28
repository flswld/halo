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

// BindCpuCore 协程绑核
func BindCpuCore(core int) bool {
	ret := C.bind_cpu_core(C.int(core))
	return ret == 0
}
