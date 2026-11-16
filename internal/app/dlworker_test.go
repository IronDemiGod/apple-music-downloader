package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/model"
)

// TestMain lets the test binary act as the download worker, the way amdl
// starts itself, with extra modes standing in for real downloads.
func TestMain(m *testing.M) {
	extraWorkerModes["test-copy"] = func(req downloadRequest) error {
		// Like the real download: progress output on stdout, then the file.
		fmt.Print("\rDecrypting... 50%\rDecrypting... 100%\nDecrypted\n")
		return os.WriteFile(req.Out, []byte("audio "+req.AdamID+" lite="+req.Config.General.LiteServer), 0o644)
	}
	extraWorkerModes["test-fail"] = func(req downloadRequest) error {
		return errors.New("segment 3: connection reset")
	}
	extraWorkerModes["test-unavailable"] = func(req downloadRequest) error {
		return errors.New("Unavailable")
	}
	if IsDownloadWorker() {
		os.Exit(RunDownloadWorker())
	}
	os.Exit(m.Run())
}

func TestDownloadWorkerProcess(t *testing.T) {
	r := &Runner{}
	r.Config.General.LiteServer = "http://127.0.0.1:12340"
	out := filepath.Join(t.TempDir(), "01. One.part.m4a")

	if err := r.downloadInWorker(downloadRequest{Mode: "test-copy", AdamID: "42", Out: out}); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The worker received the request and the config (over stdin).
	if string(got) != "audio 42 lite=http://127.0.0.1:12340" {
		t.Errorf("file = %q", got)
	}

	err = r.downloadInWorker(downloadRequest{Mode: "test-fail", Out: out})
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("failure: got %v, want the worker's message", err)
	}
	if err := r.downloadInWorker(downloadRequest{Mode: "test-unavailable", Out: out}); !errors.Is(err, errUnavailable) {
		t.Errorf("unavailable: got %v, want errUnavailable", err)
	}
}

func TestDownloadWorkerOnlyWithItsArgument(t *testing.T) {
	// A normal command line must never start worker mode.
	for _, args := range [][]string{{"amdl"}, {"amdl", "--download-worker", "x"}, {"amdl", "--atmos"}} {
		saved := os.Args
		os.Args = args
		is := IsDownloadWorker()
		os.Args = saved
		if is {
			t.Errorf("IsDownloadWorker() true for %q", args)
		}
	}
}

// fragmentedALAC creates a small fragmented ALAC .m4a, like a fresh download.
func fragmentedALAC(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "frag.m4a")
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "sine=d=2", "-c:a", "alac",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "500000", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParallelDownloadPipeline(t *testing.T) {
	sample := fragmentedALAC(t)

	const parallel = 3
	var active, maxActive int32
	var mu sync.Mutex
	order := []string{}
	saved := downloadTrack
	defer func() { downloadTrack = saved }()
	downloadTrack = func(r *Runner, req downloadRequest) error {
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		defer atomic.AddInt32(&active, -1)
		// Earlier tracks take longer, so downloads finish out of order.
		d := map[string]time.Duration{"1": 250, "2": 150, "3": 50}[req.AdamID]
		time.Sleep(d*time.Millisecond + 20*time.Millisecond)
		mu.Lock()
		order = append(order, req.AdamID)
		mu.Unlock()
		if req.AdamID == "5" {
			return errors.New("connection reset")
		}
		return os.WriteFile(req.Out, sample, 0o644)
	}

	r := convertRunner(t, fakeFFmpeg(t, ""))
	r.Config.General.ParallelDownloads = parallel
	r.Flags.Timings = true
	dir := t.TempDir()
	const tracks = 7
	for i := 1; i <= tracks; i++ {
		final := filepath.Join(dir, fmt.Sprintf("%02d. Song.m4a", i))
		part := strings.TrimSuffix(final, ".m4a") + ".part.m4a"
		tr := newTestTrack(fmt.Sprint(i), "Song", "Artist", "Album")
		job := trackJob{download: true, aacLC: true, post: true, track: tr, partPath: part, finalPath: final,
			preID: "album", taskNum: i, added: AddedTrack{Path: final, Song: fmt.Sprint(i)}}
		cj, ok := r.prepareConvert(final, "ALAC")
		if !ok {
			t.Fatal("prepareConvert refused")
		}
		job.convert, job.convertJob = true, cj
		r.queueJob(job)
	}
	r.waitJobs()

	if m := atomic.LoadInt32(&maxActive); m != parallel {
		t.Errorf("max concurrent downloads = %d, want %d", m, parallel)
	}
	if order[0] == "1" {
		t.Errorf("downloads should finish out of order, got %v", order)
	}
	if c := r.State.Counter; c.Success != tracks-1 || c.Error != 1 {
		t.Errorf("counters = %+v, want %d successes and 1 error", c, tracks-1)
	}
	// Applied in queue order, without the failed track 5.
	var got []string
	for _, a := range r.State.AddedTracks {
		got = append(got, a.Song)
		if !strings.HasSuffix(a.Path, ".flac") {
			t.Errorf("track %s path %q, want the converted .flac", a.Song, a.Path)
		}
	}
	if strings.Join(got, ",") != "1,2,3,4,6,7" {
		t.Errorf("AddedTracks order = %v", got)
	}
	if want := []int{1, 2, 3, 4, 6, 7}; fmt.Sprint(r.State.OKDict["album"]) != fmt.Sprint(want) {
		t.Errorf("OKDict = %v, want %v", r.State.OKDict["album"], want)
	}
	// Nothing temporary is left; track 5 left nothing at all.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, ".part") || strings.HasPrefix(name, "05.") || !strings.HasSuffix(name, ".flac") {
			t.Errorf("unexpected file left: %s", name)
		}
	}
	if len(entries) != tracks-1 {
		t.Errorf("%d files, want %d", len(entries), tracks-1)
	}
	if r.State.Timings.bgBytes != int64(len(sample)*(tracks-1)) {
		t.Errorf("bgBytes = %d", r.State.Timings.bgBytes)
	}
	r.printTimingsSummary()
}

func newTestTrack(id, name, artist, album string) *model.Track {
	tr := &model.Track{ID: id}
	tr.Resp.Attributes.Name = name
	tr.Resp.Attributes.ArtistName = artist
	tr.Resp.Attributes.AlbumName = album
	return tr
}
