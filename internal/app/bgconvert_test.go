package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFFmpeg writes a script that copies its "-i" input to its last argument,
// standing in for ffmpeg.
func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nin=\"\"; prev=\"\"; for a in \"$@\"; do [ \"$prev\" = -i ] && in=\"$a\"; prev=\"$a\"; out=\"$a\"; done\ncp \"$in\" \"$out\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackgroundConvert(t *testing.T) {
	r := &Runner{}
	r.Config.Convert.AfterDownload = true
	r.Config.Convert.Format = "flac"
	r.Config.Convert.FFmpegPath = fakeFFmpeg(t)

	dir := t.TempDir()
	var srcs []string
	for _, name := range []string{"01. One.m4a", "02. Two.m4a", "03. Three.m4a"} {
		src := filepath.Join(dir, name)
		if err := os.WriteFile(src, []byte("audio "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		job, ok := r.prepareConvert(src, "ALAC")
		if !ok {
			t.Fatalf("prepareConvert(%s) refused", name)
		}
		job.addedIdx = len(r.State.AddedTracks)
		if !r.queueConvert(job) {
			t.Fatalf("queueConvert(%s) refused", name)
		}
		r.State.AddedTracks = append(r.State.AddedTracks, AddedTrack{Path: src})
		srcs = append(srcs, src)
	}

	// The same file again (e.g. twice in a playlist) is not queued twice.
	job, _ := r.prepareConvert(srcs[0], "ALAC")
	if r.queueConvert(job) {
		t.Fatal("duplicate source was queued again")
	}

	r.waitConversions()

	for i, src := range srcs {
		want := strings.TrimSuffix(src, ".m4a") + ".flac"
		if got := r.State.AddedTracks[i].Path; got != want {
			t.Errorf("AddedTracks[%d].Path = %q, want %q", i, got, want)
		}
		if _, err := os.Stat(want); err != nil {
			t.Errorf("converted file missing: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Errorf("original %s should have been removed", filepath.Base(src))
		}
	}
	// Nothing left to collect once everything is applied.
	r.collectConversions()
}

func TestBackgroundConvertFailureKeepsOriginal(t *testing.T) {
	r := &Runner{}
	r.Config.Convert.AfterDownload = true
	r.Config.Convert.Format = "flac"
	failing := filepath.Join(t.TempDir(), "ffmpeg")
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as ffmpeg")
	}
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Config.Convert.FFmpegPath = failing

	src := filepath.Join(t.TempDir(), "01. One.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	job, ok := r.prepareConvert(src, "ALAC")
	if !ok {
		t.Fatal("prepareConvert refused")
	}
	job.addedIdx = 0
	r.State.AddedTracks = []AddedTrack{{Path: src}}
	r.queueConvert(job)
	r.waitConversions()

	if got := r.State.AddedTracks[0].Path; got != src {
		t.Errorf("path changed after a failed conversion: %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("original must be kept after a failed conversion: %v", err)
	}
}
