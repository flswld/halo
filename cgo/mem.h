#if defined(__WIN32)

#include <stdlib.h>
#include <Windows.h>

#define CACHE_LINE_SIZE 64

void *c_malloc(size_t size) {
    return malloc(size);
}

void c_free(void *p) {
    free(p);
}

void *aligned_malloc(size_t size) {
    return _aligned_malloc(size, CACHE_LINE_SIZE);
}

void aligned_free(void *mem) {
    _aligned_free(mem);
}

void *get_share_mem(char *name, size_t size) {
    HANDLE hMapFile = CreateFileMappingA(INVALID_HANDLE_VALUE, NULL, PAGE_READWRITE, size >> 32, size & 0xFFFFFFFF, name);
    if (hMapFile == NULL) {
        return NULL;
    }

    LPVOID pBuf = MapViewOfFile(hMapFile, FILE_MAP_ALL_ACCESS, 0, 0, size);
    if (pBuf == NULL) {
        CloseHandle(hMapFile);
        return NULL;
    }

    return pBuf;
}

#elif defined(__linux__)

#include <stdlib.h>
#include <fcntl.h>
#include <sys/mman.h>
#include <unistd.h>

#define CACHE_LINE_SIZE 64

void *c_malloc(size_t size) {
    return malloc(size);
}

void c_free(void *p) {
    free(p);
}

void *aligned_malloc(size_t size) {
    return aligned_alloc(CACHE_LINE_SIZE, size);
}

void aligned_free(void *mem) {
    free(mem);
}

void *get_share_mem(char *name, size_t size) {
    int shm_fd = shm_open(name, O_CREAT | O_RDWR, 0666);
    if (shm_fd == -1) {
        return NULL;
    }

    int ret = ftruncate(shm_fd, size);
    if (ret == -1) {
        shm_unlink(name);
        close(shm_fd);
        return NULL;
    }

    void *ptr = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_SHARED, shm_fd, 0);
    if (ptr == MAP_FAILED) {
        shm_unlink(name);
        close(shm_fd);
        return NULL;
    }

    ret = mlock(ptr, size);
    if (ret == -1) {
        munmap(ptr, size);
        shm_unlink(name);
        close(shm_fd);
        return NULL;
    }

    close(shm_fd);
    return ptr;
}

#else

#include <stdlib.h>

void *c_malloc(size_t size) {
    return malloc(size);
}

void c_free(void *p) {
    free(p);
}

void *aligned_malloc(size_t size) {
    return NULL;
}

void aligned_free(void *mem) {
    return;
}

void *get_share_mem(char *name, size_t size) {
    return NULL;
}

#endif
