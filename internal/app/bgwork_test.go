package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeFFmpeg writes a script standing in for ffmpeg: it copies its "-i" input
// to its last argument, sleeping 0.3 s first when the input name contains
// "slow" (to make jobs finish out of order).
func fakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if body == "" {
		body = "in=\"\"; prev=\"\"; for a in \"$@\"; do [ \"$prev\" = -i ] && in=\"$a\"; prev=\"$a\"; out=\"$a\"; done\n" +
			"case \"$in\" in *slow*) sleep 0.3;; esac\ncp \"$in\" \"$out\"\n"
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func convertRunner(t *testing.T, ffmpeg string) *Runner {
	r := &Runner{}
	r.State.OKDict = map[string][]int{}
	r.Config.Convert.AfterDownload = true
	r.Config.Convert.Format = "flac"
	r.Config.Convert.FFmpegPath = ffmpeg
	return r
}

// queueConvertOnly queues a convert-only job (an existing .m4a), as ripTrack
// does for a file left unconverted by an interrupted run.
func queueConvertOnly(t *testing.T, r *Runner, src string) {
	t.Helper()
	cj, ok := r.prepareConvert(src, "ALAC")
	if !ok {
		t.Fatalf("prepareConvert(%s) refused", filepath.Base(src))
	}
	r.queueJob(trackJob{finalPath: src, convert: true, convertJob: cj, added: AddedTrack{Path: src, Song: filepath.Base(src)}})
}

func TestBackgroundJobsApplyInQueueOrder(t *testing.T) {
	r := convertRunner(t, fakeFFmpeg(t, ""))
	dir := t.TempDir()
	// The first file is slow, so the second worker finishes the others first;
	// results must still be applied in queue order.
	names := []string{"01. slow.m4a", "02. Two.m4a", "03. Three.m4a", "04. Four.m4a"}
	for _, name := range names {
		src := filepath.Join(dir, name)
		if err := os.WriteFile(src, []byte("audio "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		queueConvertOnly(t, r, src)
		if !r.isInflight(src) {
			t.Fatalf("%s should be in flight", name)
		}
	}

	r.waitJobs()

	if len(r.State.AddedTracks) != len(names) {
		t.Fatalf("got %d AddedTracks, want %d", len(r.State.AddedTracks), len(names))
	}
	for i, name := range names {
		src := filepath.Join(dir, name)
		want := strings.TrimSuffix(src, ".m4a") + ".flac"
		got := r.State.AddedTracks[i]
		if got.Song != name || got.Path != want {
			t.Errorf("AddedTracks[%d] = %s %q, want %s %q", i, got.Song, got.Path, name, want)
		}
		if _, err := os.Stat(want); err != nil {
			t.Errorf("converted file missing: %v", err)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Errorf("original %s should have been removed", name)
		}
		if _, err := os.Stat(strings.TrimSuffix(src, ".m4a") + ".part.flac"); !os.IsNotExist(err) {
			t.Errorf("temporary .part.flac of %s left behind", name)
		}
		if r.isInflight(src) {
			t.Errorf("%s still in flight after waitJobs", name)
		}
	}
	// Convert-only jobs belong to tracks already counted when found on disk.
	if r.State.Counter.Success != 0 {
		t.Errorf("convert-only jobs must not count successes, got %d", r.State.Counter.Success)
	}
}

func TestBackgroundConvertFailureKeepsOriginal(t *testing.T) {
	r := convertRunner(t, fakeFFmpeg(t, "for a in \"$@\"; do out=\"$a\"; done\necho partial > \"$out\"\nexit 1\n"))
	src := filepath.Join(t.TempDir(), "01. One.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	queueConvertOnly(t, r, src)
	r.waitJobs()

	if got := r.State.AddedTracks[0].Path; got != src {
		t.Errorf("path changed after a failed conversion: %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("original must be kept after a failed conversion: %v", err)
	}
	base := strings.TrimSuffix(src, ".m4a")
	for _, p := range []string{base + ".flac", base + ".part.flac"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must not exist after a failed conversion", filepath.Base(p))
		}
	}
}

// A download that cannot be processed is counted as an error and its
// temporary file removed, so a retry downloads it again instead of finding a
// broken file under the final name.
func TestBackgroundPostFailureRemovesPartFile(t *testing.T) {
	r := &Runner{}
	r.State.OKDict = map[string][]int{}
	dir := t.TempDir()
	part := filepath.Join(dir, "01. One.part.m4a")
	final := filepath.Join(dir, "01. One.m4a")
	if err := os.WriteFile(part, []byte("not an mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.queueJob(trackJob{post: true, partPath: part, finalPath: final, preID: "123", taskNum: 1})
	r.waitJobs()

	if r.State.Counter.Error != 1 || r.State.Counter.Success != 0 {
		t.Errorf("counters = %+v, want 1 error", r.State.Counter)
	}
	if len(r.State.AddedTracks) != 0 || len(r.State.OKDict["123"]) != 0 {
		t.Error("a failed track must not be added or marked done")
	}
	for _, p := range []string{part, final} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s must not exist after a failed job", filepath.Base(p))
		}
	}
}

func TestCollectJobsDoesNotWait(t *testing.T) {
	r := convertRunner(t, fakeFFmpeg(t, ""))
	dir := t.TempDir()
	src := filepath.Join(dir, "01. slow.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	queueConvertOnly(t, r, src)
	start := time.Now()
	r.collectJobs()
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("collectJobs blocked for %v", d)
	}
	if len(r.State.AddedTracks) != 0 {
		t.Error("an unfinished job must not be applied yet")
	}
	r.waitJobs()
	if len(r.State.AddedTracks) != 1 {
		t.Error("waitJobs must apply the job")
	}
}

func TestParallelConverts(t *testing.T) {
	r := &Runner{}
	if got := r.parallelConverts(); got != autoConverts || got < 2 || got > 6 {
		t.Errorf("auto = %d (autoConverts %d), want 2..6", got, autoConverts)
	}
	for in, want := range map[int]int{1: 1, 4: 4, 100: 32, -3: autoConverts} {
		r.Config.Convert.Parallel = in
		if got := r.parallelConverts(); got != want {
			t.Errorf("convert.parallel %d -> %d, want %d", in, got, want)
		}
	}
}
