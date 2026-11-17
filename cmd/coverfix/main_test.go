package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanFolderArgs(t *testing.T) {
	got, err := cleanFolderArgs([]string{`C:\Music\Some Artist\Some Album - Single"`, `C:\Music\Another Artist`}, true)
	if err != nil || got[0] != `C:\Music\Some Artist\Some Album - Single` || got[1] != `C:\Music\Another Artist` {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := cleanFolderArgs([]string{`C:\A" C:\B`}, true); err == nil {
		t.Fatal("quote in the middle: want error")
	}
	if got, _ := cleanFolderArgs([]string{`/music/odd"name"`}, false); got[0] != `/music/odd"name"` {
		t.Fatalf("non-Windows args must be left alone, got %q", got)
	}
}

func TestResolveIn(t *testing.T) {
	base := t.TempDir()
	for _, d := range []string{"ADO", "Pink Floyd", "King Gnu", "Hello, World"} {
		if err := os.Mkdir(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dirs, missing := resolveIn(base, []string{"ADO,Pink Floyd, King Gnu", "Hello, World", "Nope"})
	var names []string
	for _, d := range dirs {
		names = append(names, filepath.Base(d))
	}
	if strings.Join(names, "|") != "ADO|Pink Floyd|King Gnu|Hello, World" {
		t.Errorf("dirs = %q", names)
	}
	if len(missing) != 1 || missing[0] != "Nope" {
		t.Errorf("missing = %q", missing)
	}
	if dirs, _ := resolveIn(base, nil); len(dirs) != 1 || dirs[0] != base {
		t.Errorf("no names: %q, want the base folder", dirs)
	}
}
