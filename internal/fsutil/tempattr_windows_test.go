//go:build windows

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// os.Create on a file created by CreateTemporary keeps the temporary
// attribute: the download relies on this to stay in memory.
func TestTemporaryAttributeSurvivesCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01. Song.part.m4a")
	if err := CreateTemporary(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("audio")
	f.Close()
	if !IsTemporary(path) {
		t.Fatal("os.Create dropped the temporary attribute")
	}
	if err := ClearTemporary(path); err != nil || IsTemporary(path) {
		t.Fatalf("ClearTemporary: %v", err)
	}
}
