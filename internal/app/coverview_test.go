package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

func TestCoverViewFrame(t *testing.T) {
	v := &coverView{out: &bytes.Buffer{}, total: 412, finished: 37, failed: 3, start: time.Now().Add(-time.Minute),
		active: map[int]*coverAlbum{}}
	v.active[40] = &coverAlbum{name: "YOASOBI/THE BOOK 3", step: "embedding", done: 9, total: 14}
	v.active[41] = &coverAlbum{name: "米津玄師/LOST CORNER とても長いアルバム名", step: "looking up the album"}
	for _, w := range []int{40, 80, 120} {
		lines := v.frame(w)
		if len(lines) != 4 {
			t.Fatalf("width %d: %d lines, want 4", w, len(lines))
		}
		for _, l := range lines {
			if cw := runewidth.StringWidth(visible(l)); cw > w-1 {
				t.Errorf("width %d: line %d cols: %q", w, cw, visible(l))
			}
		}
	}
	all := visible(strings.Join(v.frame(120), "\n"))
	for _, want := range []string{"37/412 albums", "3 failed", "9%", "left", "9/14 tracks  embedding", "looking up the album"} {
		if !strings.Contains(all, want) {
			t.Errorf("frame lacks %q:\n%s", want, all)
		}
	}
}

func TestCoverViewLogsAboveBars(t *testing.T) {
	buf := &bytes.Buffer{}
	v := &coverView{out: buf, total: 2, start: time.Now(), active: map[int]*coverAlbum{}}
	p := v.begin(0, "A/One")
	p.tracks(1, 2)
	v.render()
	v.end(0, false)
	v.log("✔ A/One: cover.png, embedded in 2/2 tracks\n")
	buf.Reset()
	v.render()
	out := buf.String()
	if !strings.Contains(out, "✔ A/One") || !strings.Contains(out, "1/2 albums") {
		t.Errorf("log line or updated header missing: %q", visible(out))
	}
	if strings.Index(out, "✔ A/One") > strings.Index(out, "1/2 albums") {
		t.Error("the log line must come before the bars")
	}
	var nilView *coverView
	nilView.begin(0, "x").step("ok") // a nil view and progress do nothing
	nilView.end(0, true)
	nilView.finish()
}
