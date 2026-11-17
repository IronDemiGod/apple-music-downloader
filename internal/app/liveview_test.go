package app

import (
	"amdl/internal/model"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func visible(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestParseProgress(t *testing.T) {
	// Exactly what the download's progress bar writes (captured from the
	// progressbar options used by fairplay-rip).
	out := "\rDecrypting...   2%  (1.0/40 MB, 244 GB/s) \r                                          \r" +
		"\rDecrypting...  35%  (14/40 MB, 1.4 GB/s) \r                                          \r" +
		"\rDecrypting... 100%  (40/40 MB, 1.3 GB/s) [0s]\nDecrypted\n"
	var got []string
	parseProgress(strings.NewReader(out), func(done, total int64) {
		got = append(got, fmt.Sprintf("%d/%d", done, total))
	})
	want := "1000000/40000000 14000000/40000000 40000000/40000000"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

func TestFormatQuality(t *testing.T) {
	cases := []struct {
		q          string
		atmos, aac bool
		want       string
	}{
		{"24B-192.0kHz", false, false, "ALAC 24-bit/192 kHz"},
		{"24B-44.1kHz", false, false, "ALAC 24-bit/44.1 kHz"},
		{"16B-44.1kHz", false, false, "ALAC 16-bit/44.1 kHz"},
		{"2768 Kbps", true, false, "Atmos 2768 kbps"},
		{"256 Kbps", false, true, "AAC 256 kbps"},
		{"", false, false, ""},
	}
	for _, c := range cases {
		if got := formatQuality(c.q, c.atmos, c.aac); got != c.want {
			t.Errorf("formatQuality(%q) = %q, want %q", c.q, got, c.want)
		}
	}
}

func testView(items ...liveItem) *liveView {
	v := &liveView{title: "ADO — Vivarium", out: &bytes.Buffer{}}
	for i := range items {
		it := items[i]
		v.items = append(v.items, &it)
	}
	return v
}

func TestFrameFitsWidth(t *testing.T) {
	now := time.Now()
	v := testView(
		liveItem{num: 1, name: "あれからどれくらい経ったことだろう、とても長い曲名のテスト", artist: "ADO", quality: "ALAC 24-bit/96 kHz", state: liveDone, downloadB: 64_200_000, finished: 4100 * time.Millisecond},
		liveItem{num: 2, name: "Vivarium", artist: "ADO", quality: "ALAC 24-bit/96 kHz", state: liveDownloading, done: 33_100_000, total: 63_800_000, started: now.Add(-2 * time.Second)},
		liveItem{num: 3, name: "Song Three", artist: "Artist", state: liveWaiting},
		liveItem{num: 4, name: "Broken", artist: "Artist", state: liveFailed, step: "Failed to download: connection reset by peer while reading segment 12 of 40"},
	)
	for _, width := range []int{40, 60, 80, 120} {
		lines := v.frame(width, 50)
		if len(lines) != 3 { // header, overall bar, the one download
			t.Fatalf("width %d: %d lines, want 3", width, len(lines))
		}
		for _, l := range lines {
			if w := runewidth.StringWidth(visible(l)); w > width-1 {
				t.Errorf("width %d: line is %d columns: %q", width, w, visible(l))
			}
		}
	}
	lines := v.frame(100, 50)
	for _, want := range []string{"1/4 songs · 1 failed", "1 downloading", "02. Vivarium — ADO", "52%", "33.1 MB/63.8 MB", "ALAC 24-bit/96 kHz"} {
		if !strings.Contains(visible(strings.Join(lines, "\n")), want) {
			t.Errorf("frame lacks %q:\n%s", want, visible(strings.Join(lines, "\n")))
		}
	}
	if all := visible(strings.Join(lines, "\n")); strings.Contains(all, "Song Three") || strings.Contains(all, "Broken") {
		t.Errorf("waiting and finished tracks must not be listed:\n%s", all)
	}
	if testing.Verbose() {
		fmt.Println(strings.Join(lines, "\n") + ansiReset)
	}
}

func TestFrameFoldsLongLists(t *testing.T) {
	var items []liveItem
	for i := 1; i <= 35; i++ {
		st := liveDone
		if i > 5 {
			st = liveDownloading
		}
		items = append(items, liveItem{num: i, name: fmt.Sprintf("Track %d", i), state: st})
	}
	v := testView(items...)
	lines := v.frame(80, 24)
	if len(lines) > 24-1 {
		t.Fatalf("%d lines do not fit a 24-line window", len(lines))
	}
	all := visible(strings.Join(lines, "\n"))
	if !strings.Contains(all, "5/35 songs") || !strings.Contains(all, "… 10 more") {
		t.Errorf("expected the song count and a more line:\n%s", all)
	}
}

func TestRenderRedrawsInPlace(t *testing.T) {
	v := testView(liveItem{num: 1, name: "One", state: liveWaiting}, liveItem{num: 2, name: "Two", state: liveWaiting})
	v.render()
	buf := v.out.(*bytes.Buffer)
	first := buf.Len()
	buf.Reset()
	v.render()
	if buf.Len() != 0 {
		t.Errorf("an unchanged frame must not be redrawn, got %q", buf.String())
	}
	v.items[0].state = liveDone
	v.render()
	// A changed frame moves the cursor up over the 2 lines of the first
	// (header and overall bar; waiting tracks are not listed).
	if !strings.HasPrefix(buf.String(), "\x1b[?2026h\x1b[2A") {
		t.Errorf("changed frame does not start by moving up 2 lines: %q", buf.String()[:16])
	}
	if first == 0 {
		t.Error("first frame is empty")
	}
}

// The pipeline updates a track's entry from waiting to done, with the size
// and quality, when a view is attached to its job.
func TestPipelineUpdatesLiveItems(t *testing.T) {
	sample := fragmentedALAC(t)
	saved := downloadTrack
	defer func() { downloadTrack = saved }()
	downloadTrack = func(r *Runner, req downloadRequest, progress func(done, total int64)) error {
		if progress == nil {
			t.Error("no progress callback with a live view")
		} else {
			progress(int64(len(sample))/2, int64(len(sample)))
		}
		if req.AdamID == "2" {
			return fmt.Errorf("connection reset")
		}
		return os.WriteFile(req.Out, sample, 0o644)
	}

	r := convertRunner(t, fakeFFmpeg(t, ""))
	v := testView(liveItem{num: 1, name: "One"}, liveItem{num: 2, name: "Two"})
	r.live = v
	dir := t.TempDir()
	for i := 1; i <= 2; i++ {
		final := filepath.Join(dir, fmt.Sprintf("%02d. Song.m4a", i))
		job := trackJob{download: true, aacLC: true, post: true, track: newTestTrack(fmt.Sprint(i), "Song", "A", "B"),
			partPath: strings.TrimSuffix(final, ".m4a") + ".part.m4a", finalPath: final, preID: "x", taskNum: i,
			view: v, item: v.item(i)}
		job.convertJob, job.convert = r.prepareConvert(final, "ALAC")
		r.queueJob(job)
	}
	r.waitJobs()

	one, two := v.item(1), v.item(2)
	if one.state != liveDone || one.downloadB != int64(len(sample)) || one.quality != "AAC 256 kbps" {
		t.Errorf("item 1 = state %d, %d bytes, quality %q", one.state, one.downloadB, one.quality)
	}
	if two.state != liveFailed || !strings.Contains(two.step, "connection reset") || len(two.notes) == 0 {
		t.Errorf("item 2 = state %d, step %q, notes %v", two.state, two.step, two.notes)
	}
}

func TestAlbumIDFromSongURL(t *testing.T) {
	cases := map[string]string{
		"https://music.apple.com/us/album/vivarium/1640123456?i=1640123457": "1640123456",
		"https://music.apple.com/jp/album/1640123456?i=1":                   "1640123456",
		"https://music.apple.com/us/song/vivarium/1640123457":               "",
		"https://music.apple.com/us/album/name/pl.abc":                      "",
		"": "",
	}
	for in, want := range cases {
		if got := albumIDFromSongURL(in); got != want {
			t.Errorf("albumIDFromSongURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A playlist's list holds its tracks by ID: downloaded as part of their
// albums, they have the albums' track numbers, and a song listed twice gets
// both entries.
func TestItemForByID(t *testing.T) {
	v := testView(
		liveItem{id: "111", num: 1, name: "A"},
		liveItem{id: "222", num: 2, name: "B"},
		liveItem{id: "111", num: 3, name: "A again"},
	)
	if it := v.itemFor(&model.Track{ID: "222", TaskNum: 7}); it == nil || it.name != "B" {
		t.Fatalf("by ID: got %+v", it)
	}
	first := v.itemFor(&model.Track{ID: "111", TaskNum: 1})
	second := v.itemFor(&model.Track{ID: "111", TaskNum: 1})
	if first == nil || second == nil || first == second {
		t.Fatalf("a song listed twice must get both entries: %v %v", first, second)
	}
	if v.itemFor(&model.Track{ID: "111", TaskNum: 1}) != nil {
		t.Fatal("all entries of 111 are claimed")
	}
	v.failItem("333", "x") // unknown ID: nothing happens
}
