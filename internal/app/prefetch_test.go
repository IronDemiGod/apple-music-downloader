package app

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

// The prefetch and the real call share one download; the real call takes it.
func TestCoverCacheDownloadsOnce(t *testing.T) {
	c := &coverCache{dir: t.TempDir(), m: map[string]*coverEntry{}}
	var downloads atomic.Int32
	dl := func(f *os.File) error { downloads.Add(1); _, err := f.WriteString("png"); return err }
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.get("u", false, dl) }()
	}
	wg.Wait()
	e := c.get("u", true, dl)
	if downloads.Load() != 1 || e.err != nil {
		t.Fatalf("downloads = %d, err = %v", downloads.Load(), e.err)
	}
	if b, _ := os.ReadFile(e.path); string(b) != "png" {
		t.Fatalf("cached file = %q", b)
	}
	if len(c.m) != 0 {
		t.Fatal("a taken entry must leave the cache")
	}
}
