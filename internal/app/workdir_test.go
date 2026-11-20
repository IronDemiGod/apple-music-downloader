package app

import (
	"amdl/internal/config"

	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("AMDL_TEST_DIR", "/x/y")
	cases := map[string]string{
		"":                     "",
		"~/amdl":               filepath.Join(home, "amdl"),
		"$AMDL_TEST_DIR/w":     filepath.Clean("/x/y/w"),
		"%AMDL_TEST_DIR%/w":    filepath.Clean("/x/y/w"),
		"/dev/shm/amdl":        "/dev/shm/amdl",
		"%NO_SUCH_VAR_AMDL%/w": filepath.Clean("%NO_SUCH_VAR_AMDL%/w"),
	}
	for in, want := range cases {
		if got := expandPath(in); got != want {
			t.Errorf("expandPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// moveFile across filesystems (RAM to the home folder on Linux) copies via a
// temporary name and removes the source.
func TestMoveFileAcrossFilesystems(t *testing.T) {
	srcRoot := t.TempDir()
	if runtime.GOOS == "linux" {
		if d, err := os.MkdirTemp("/dev/shm", "amdl-test-"); err == nil {
			srcRoot = d
			defer os.RemoveAll(d)
		}
	}
	dstDir := t.TempDir()
	home, _ := os.UserHomeDir()
	if d, err := os.MkdirTemp(home, ".amdl-test-"); err == nil {
		dstDir = d
		defer os.RemoveAll(d)
	}
	src := filepath.Join(srcRoot, "01. Song.flac")
	data := []byte(strings.Repeat("flac", 100000))
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dstDir, "01. Song.flac")
	if err := moveFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || len(got) != len(data) {
		t.Fatalf("dst: %d bytes, %v", len(got), err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source not removed")
	}
	if _, err := os.Stat(filepath.Join(dstDir, "01. Song.part.flac")); !os.IsNotExist(err) {
		t.Error("temporary file left")
	}
}

func TestRemoveDeadRuns(t *testing.T) {
	root := t.TempDir()
	dead := filepath.Join(root, "run-999999") // no such PID
	mine := filepath.Join(root, fmt.Sprintf("run-%d", os.Getpid()))
	other := filepath.Join(root, "notes")
	for _, d := range []string{dead, mine, other} {
		os.MkdirAll(filepath.Join(d, "123"), 0o700)
	}
	removeDeadRuns(root)
	if _, err := os.Stat(dead); !os.IsNotExist(err) && pidAlive(999999) == false {
		t.Error("dead run folder not removed")
	}
	for _, d := range []string{mine, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s must be kept", filepath.Base(d))
		}
	}
}

// The whole pipeline with a work folder: files are processed there and only
// the finished FLAC reaches the save folder; the work folder ends up empty.
func TestPipelineWithWorkDir(t *testing.T) {
	sample := fragmentedALAC(t)
	saved := downloadTrack
	defer func() { downloadTrack = saved }()
	downloadTrack = func(r *Runner, req downloadRequest, progress func(done, total int64)) error {
		if req.AdamID == "3" {
			return fmt.Errorf("connection reset")
		}
		return os.WriteFile(req.Out, sample, 0o644)
	}

	r := convertRunner(t, fakeFFmpeg(t, ""))
	r.Config.General.ParallelDownloads = 2
	work := t.TempDir()
	r.Config.Paths.WorkDir = work
	save := t.TempDir()
	for i := 1; i <= 3; i++ {
		final := filepath.Join(save, fmt.Sprintf("%02d. Song.m4a", i))
		wd := r.trackWorkDir(fmt.Sprint(i))
		if wd == "" {
			t.Fatal("no work folder")
		}
		job := trackJob{download: true, aacLC: true, post: true, track: newTestTrack(fmt.Sprint(i), "Song", "A", "B"),
			partPath: filepath.Join(wd, fmt.Sprintf("%02d. Song.part.m4a", i)), finalPath: final, workDir: wd,
			preID: "x", taskNum: i, added: AddedTrack{Path: final}}
		job.convertJob, job.convert = r.prepareConvert(filepath.Join(wd, filepath.Base(final)), "ALAC")
		r.queueJob(job)
	}
	r.waitJobs()

	if c := r.State.Counter; c.Success != 2 || c.Error != 1 {
		t.Errorf("counters = %+v", c)
	}
	entries, _ := os.ReadDir(save)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "01. Song.flac,02. Song.flac" {
		t.Errorf("save folder = %v, want only the two FLACs", names)
	}
	for _, a := range r.State.AddedTracks {
		if filepath.Dir(a.Path) != save || !strings.HasSuffix(a.Path, ".flac") {
			t.Errorf("AddedTracks path %q", a.Path)
		}
	}
	r.removeWorkRun()
	left, _ := os.ReadDir(work)
	if len(left) != 0 {
		t.Errorf("work folder not empty: %v", left)
	}
}

// The same song twice in a list gets two work folders: queueing the second
// copy must not delete the first copy's folder while it is still in use.
func TestTrackWorkDirUniquePerJob(t *testing.T) {
	r := NewRunner(config.ConfigSet{})
	r.Config.Paths.WorkDir = t.TempDir()
	a := r.trackWorkDir("1640123457")
	if err := os.WriteFile(filepath.Join(a, "x.part.m4a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := r.trackWorkDir("1640123457")
	if a == "" || b == "" || a == b {
		t.Fatalf("work folders %q and %q", a, b)
	}
	if _, err := os.Stat(filepath.Join(a, "x.part.m4a")); err != nil {
		t.Errorf("first copy's file is gone: %v", err)
	}
}
