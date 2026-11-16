// Package fsutil holds small filesystem helpers shared across packages.
package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// OutputPerm is the mode given to files that end up in the user's library.
const OutputPerm os.FileMode = 0o644

// CreateOutputTemp is os.CreateTemp for files that will be renamed into their
// final place. os.CreateTemp creates files as 0600, which would carry over to
// the renamed output and leave it unreadable to other users (e.g. a media
// server running under its own account).
func CreateOutputTemp(dir, pattern string) (*os.File, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(OutputPerm); err != nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return nil, err
	}
	return f, nil
}

// MkdirAll is os.MkdirAll, except that a parent folder Windows reports as
// already existing is accepted even when it cannot be stat'ed. os.MkdirAll
// stats each parent first; on Windows that can fail for a folder that does
// exist (OneDrive-managed or security-filtered folders such as Documents), and
// os.MkdirAll then fails with "Cannot create a file when that file already
// exists" instead of creating the folders below it.
func MkdirAll(path string, perm os.FileMode) error {
	err := os.MkdirAll(path, perm)
	if err == nil {
		return nil
	}
	if fi, statErr := os.Stat(path); statErr == nil && fi.IsDir() {
		return nil
	}

	clean := filepath.Clean(path)
	vol := filepath.VolumeName(clean)
	rest := clean[len(vol):]
	cur := vol
	if strings.HasPrefix(rest, string(filepath.Separator)) {
		cur += string(filepath.Separator)
	}
	parts := strings.Split(rest, string(filepath.Separator))
	for i, part := range parts {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		mkErr := os.Mkdir(cur, perm)
		if mkErr == nil {
			continue
		}
		if !os.IsExist(mkErr) {
			return mkErr
		}
		// It exists. For the target itself, make sure it is not a file.
		if i == len(parts)-1 {
			if fi, statErr := os.Stat(cur); statErr == nil && !fi.IsDir() {
				return &os.PathError{Op: "mkdir", Path: cur, Err: os.ErrExist}
			}
		}
	}
	return nil
}
