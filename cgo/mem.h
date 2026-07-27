#if defined(__WIN32)

#include <stdlib.h>
#include <Windows.h>

// c_malloc 从 C 运行时堆分配指定字节数的内存
void *c_malloc(size_t size) {
    return malloc(size);
}

// c_free 释放 C 运行时堆内存
void c_free(void *p) {
    free(p);
}

// aligned_malloc 分配按指定边界对齐的内存
void *aligned_malloc(size_t size, size_t align) {
    return _aligned_malloc(size, align);
}

// aligned_free 释放对齐分配的内存
void aligned_free(void *mem) {
    _aligned_free(mem);
}

// get_share_mem 获取或创建指定名称和大小的共享内存
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

// c_malloc 从 C 运行时堆分配指定字节数的内存
void *c_malloc(size_t size) {
    return malloc(size);
}

// c_free 释放 C 运行时堆内存
void c_free(void *p) {
    free(p);
}

// aligned_malloc 分配按指定边界对齐的内存
void *aligned_malloc(size_t size, size_t align) {
    return aligned_alloc(align, size);
}

// aligned_free 释放对齐分配的内存
void aligned_free(void *mem) {
    free(mem);
}

// get_share_mem 获取或创建指定名称和大小的共享内存
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
#include <fcntl.h>
#include <sys/mman.h>
#include <unistd.h>

// c_malloc 从 C 运行时堆分配指定字节数的内存
void *c_malloc(size_t size) {
    return malloc(size);
}

// c_free 释放 C 运行时堆内存
void c_free(void *p) {
    free(p);
}

// aligned_malloc 分配按指定边界对齐的内存
void *aligned_malloc(size_t size, size_t align) {
    void *mem = NULL;
    int ret = posix_memalign(&mem, align, size);
    if (ret != 0) {
        return NULL;
    }
    return mem;
}

// aligned_free 释放对齐分配的内存
void aligned_free(void *mem) {
    free(mem);
}

// get_share_mem 获取或创建指定名称和大小的共享内存
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

#endif
