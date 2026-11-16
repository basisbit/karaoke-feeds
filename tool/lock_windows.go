//go:build windows

package kfeed

import (
	"errors"
	"syscall"
)

const errSharingViolation syscall.Errno = 32

// lockFile opens path without sharing it. The system closes the file when the process ends, also after a crash.
func lockFile(path string) (unlock func(), err error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errSharingViolation) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() { syscall.CloseHandle(h) }, nil
}
