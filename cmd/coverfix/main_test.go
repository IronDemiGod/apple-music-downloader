package main

import "testing"

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
