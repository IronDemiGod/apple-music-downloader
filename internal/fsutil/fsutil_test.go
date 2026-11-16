package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreateOutputTempPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	f, err := CreateOutputTemp(t.TempDir(), "x-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != OutputPerm {
		t.Fatalf("mode = %o, want %o", got, OutputPerm)
	}
	if _, err := os.Stat(f.Name()); err != nil {
		t.Fatal(err)
	}
}

func TestMkdirAll(t *testing.T) {
	base := t.TempDir()

	nested := filepath.Join(base, "Artist", "Album")
	if err := MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(new nested) = %v", err)
	}
	if fi, err := os.Stat(nested); err != nil || !fi.IsDir() {
		t.Fatalf("nested dir not created: %v", err)
	}
	if err := MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(existing) = %v", err)
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(file, 0o755); err == nil {
		t.Fatal("MkdirAll(existing file) = nil, want error")
	}
	if err := MkdirAll(filepath.Join(file, "sub"), 0o755); err == nil {
		t.Fatal("MkdirAll(under a file) = nil, want error")
	}
}
