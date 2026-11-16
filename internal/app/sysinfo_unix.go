//go:build unix

package app

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// freeBytes returns the space available to this user on path's filesystem.
func freeBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// pidAlive reports whether a process with this PID exists.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
