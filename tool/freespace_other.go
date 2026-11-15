//go:build !linux && !darwin && !windows

package kfeed

import "errors"

// FreeSpace is not available on this system.
func FreeSpace(dir string) (int64, error) {
	return 0, errors.New("free space: not supported on this system")
}
