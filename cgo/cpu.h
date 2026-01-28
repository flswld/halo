#if defined(__WIN32)

#include <Windows.h>

int bind_cpu_core(int *core_list, int len) {
    DWORD_PTR mask = 0;
    for (int i = 0; i < len; i++) {
        mask |= 1 << core_list[i];
    }
    HANDLE hThread = GetCurrentThread();
    DWORD_PTR ret = SetThreadAffinityMask(hThread, mask);
    if (ret == 0) {
        return -1;
    }
    return 0;
}

#elif defined(__linux__)

#define _GNU_SOURCE
#include <sched.h>
#include <pthread.h>

int bind_cpu_core(int *core_list, int len) {
    cpu_set_t mask;
    CPU_ZERO(&mask);
    for (int i = 0; i < len; i++) {
        CPU_SET(core_list[i], &mask);
    }
    pthread_t thread = pthread_self();
    int ret = pthread_setaffinity_np(thread, sizeof(mask), &mask);
    return ret;
}
#undef _GNU_SOURCE

#else

int bind_cpu_core(int *core_list, int len) {
    return -1;
}

#endif
