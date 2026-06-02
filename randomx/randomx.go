//go:build randomx && cgo

package randomx

/*
#cgo CFLAGS: -I../build/_workspace/RandomX/src
#cgo LDFLAGS: -L../build/_workspace/RandomX/build -lrandomx -lstdc++ -lm
#include <stdlib.h>
#include "randomx.h"
*/
import "C"
import "unsafe"

const FlagFullMem = int(C.RANDOMX_FLAG_FULL_MEM)

type Cache struct {
    ptr *C.randomx_cache
}

type VM struct {
    ptr *C.randomx_vm
}

func NewCache(key []byte) *Cache {
    cache := C.randomx_alloc_cache(C.RANDOMX_FLAG_DEFAULT)
    if cache == nil {
        return nil
    }

    var keyPtr unsafe.Pointer
    if len(key) > 0 {
        keyPtr = unsafe.Pointer(&key[0])
    }
    C.randomx_init_cache(cache, keyPtr, C.size_t(len(key)))
    return &Cache{ptr: cache}
}

func NewVM(cache *Cache, _ interface{}, flags int) *VM {
    if cache == nil || cache.ptr == nil {
        return nil
    }

    vm := C.randomx_create_vm(C.randomx_flags(flags), cache.ptr, nil)
    if vm == nil {
        return nil
    }
    return &VM{ptr: vm}
}

func (vm *VM) CalculateHash(input, output []byte) {
    if vm == nil || vm.ptr == nil || len(output) == 0 {
        return
    }

    var inputPtr unsafe.Pointer
    if len(input) > 0 {
        inputPtr = unsafe.Pointer(&input[0])
    }
    C.randomx_calculate_hash(vm.ptr, inputPtr, C.size_t(len(input)), unsafe.Pointer(&output[0]))
}

func (vm *VM) Close() {
    if vm == nil || vm.ptr == nil {
        return
    }

    C.randomx_destroy_vm(vm.ptr)
    vm.ptr = nil
}

func (cache *Cache) Close() {
    if cache == nil || cache.ptr == nil {
        return
    }

    C.randomx_release_cache(cache.ptr)
    cache.ptr = nil
}
