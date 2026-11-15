//go:build windows

package kfeed

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// FreeSpace returns the free bytes on the drive that holds dir, as far as the current user may use them.
func FreeSpace(dir string) (int64, error) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free uint64
	if ok, _, err := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0); ok == 0 {
		return 0, err
	}
	return int64(free), nil
}
