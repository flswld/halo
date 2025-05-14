#include <stdlib.h>

#define CACHE_LINE_SIZE 64

void *c_malloc(size_t size) {
    return malloc(size);
}

void c_free(void *p) {
    free(p);
}

void *aligned_malloc(size_t size) {
#if defined(__WIN32)
    return _aligned_malloc(size, CACHE_LINE_SIZE);
#elif defined(__linux__)
    return aligned_alloc(CACHE_LINE_SIZE, size);
#else
    return NULL;
#endif
}

void aligned_free(void *mem) {
#if defined(__WIN32)
    _aligned_free(mem);
#elif defined(__linux__)
    free(mem);
#else
    return;
#endif
}
