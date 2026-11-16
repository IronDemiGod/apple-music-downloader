//go:build windows

package fsutil

import (
	"golang.org/x/sys/windows"
)

// Windows keeps files with FILE_ATTRIBUTE_TEMPORARY in memory and avoids
// writing them to disk while enough memory is available. amdl gives its work
// files this attribute, so a track's download and processing stay in RAM and
// only the finished file is written to disk.

func attrs(path string) (uint32, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	a, err := windows.GetFileAttributes(p)
	if err != nil {
		return 0, false
	}
	return a, true
}

func setAttrs(path string, a uint32) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.SetFileAttributes(p, a)
}

// IsTemporary reports whether path has the temporary attribute.
func IsTemporary(path string) bool {
	a, ok := attrs(path)
	return ok && a&windows.FILE_ATTRIBUTE_TEMPORARY != 0
}

// MarkTemporary gives path the temporary attribute.
func MarkTemporary(path string) error {
	a, ok := attrs(path)
	if !ok {
		return nil
	}
	if a == windows.FILE_ATTRIBUTE_NORMAL {
		a = 0
	}
	return setAttrs(path, a|windows.FILE_ATTRIBUTE_TEMPORARY)
}

// ClearTemporary removes the temporary attribute, so the file is written to
// disk normally (a finished file leaving the work folder).
func ClearTemporary(path string) error {
	a, ok := attrs(path)
	if !ok || a&windows.FILE_ATTRIBUTE_TEMPORARY == 0 {
		return nil
	}
	a &^= windows.FILE_ATTRIBUTE_TEMPORARY
	if a == 0 {
		a = windows.FILE_ATTRIBUTE_NORMAL
	}
	return setAttrs(path, a)
}

// CreateTemporary creates an empty file with the temporary attribute.
// Opening it again later with os.Create keeps the attribute (Go opens
// existing files and truncates them instead of replacing them).
func CreateTemporary(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_TEMPORARY, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

// MarkHidden hides a folder (the work folder inside a save folder).
func MarkHidden(path string) error {
	a, ok := attrs(path)
	if !ok {
		return nil
	}
	return setAttrs(path, a|windows.FILE_ATTRIBUTE_HIDDEN)
}
