//go:build linux || darwin

package kfeed

import "syscall"

// FreeSpace returns the free bytes on the drive that holds dir, as far as an ordinary user may use them.
func FreeSpace(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), nil
}
