//go:build !windows

package fsutil

// The temporary file attribute exists only on Windows; elsewhere a work folder
// in RAM (e.g. /dev/shm) does the same job, so these do nothing.

func IsTemporary(path string) bool      { return false }
func MarkTemporary(path string) error   { return nil }
func ClearTemporary(path string) error  { return nil }
func CreateTemporary(path string) error { return nil }
func MarkHidden(path string) error      { return nil }
