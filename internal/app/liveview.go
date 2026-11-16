package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"amdl/internal/model"
)

// Live progress list: while an album or playlist downloads, every track gets
// two lines that are redrawn in place (like `docker pull`):
//
//	01. Song name — Artist                              ALAC 24-bit/96 kHz
//	    ███████████████░░░░░░░░░░░░░░░   52%  33.1/63.8 MB  17.2 MB/s
//
// It is used only when stdout is a terminal (not with --no-live or --debug).
// Downloads then run in child processes (dlworker.go) whose progress output
// is parsed for the percentage and sizes; nothing in the download code changes.

type liveState int

const (
	liveWaiting liveState = iota
	liveDownloading
	liveProcessing
	liveConverting
	liveDone
	liveExists
	liveFailed
)

type liveItem struct {
	num     int
	name    string
	artist  string
	quality string

	state     liveState
	step      string // processing step, or the reason for liveFailed
	note      string // shown after "done", e.g. "no lyrics"
	done      int64  // bytes downloaded
	total     int64  // bytes to download (0 = unknown)
	started   time.Time
	finished  time.Duration // download time, once downloaded
	notes     []string      // messages printed after the list (failed tracks)
	downloadB int64         // final size, for the "done" line
}

type liveView struct {
	title string
	out   io.Writer

	mu    sync.Mutex
	items []*liveItem
	lines int // lines drawn by the last frame

	stop chan struct{}
	done chan struct{}
}

// liveSupported reports whether the live list can be shown: stdout must be a
// terminal that understands ANSI cursor movement.
func (r *Runner) liveSupported() bool {
	if r.Flags.NoLive || r.Flags.Debug || r.Flags.PrintJSON {
		return false
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	return enableVT()
}

// startLive starts the live list for the given tracks, or returns nil when it
// cannot be shown.
func (r *Runner) startLive(title string, tracks []liveItem) *liveView {
	if !r.liveSupported() || len(tracks) == 0 {
		return nil
	}
	v := &liveView{title: title, out: os.Stdout, stop: make(chan struct{}), done: make(chan struct{})}
	for i := range tracks {
		it := tracks[i]
		v.items = append(v.items, &it)
	}
	v.render()
	go func() {
		defer close(v.done)
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-v.stop:
				return
			case <-t.C:
				v.render()
			}
		}
	}()
	return v
}

// item returns the list entry for track number num (nil if not listed).
func (v *liveView) item(num int) *liveItem {
	if v == nil {
		return nil
	}
	for _, it := range v.items {
		if it.num == num {
			return it
		}
	}
	return nil
}

// update changes an item under the view's lock; safe from any goroutine and
// a no-op for a nil view or item.
func (v *liveView) update(it *liveItem, f func(*liveItem)) {
	if v == nil || it == nil {
		return
	}
	v.mu.Lock()
	f(it)
	v.mu.Unlock()
}

// finish draws the final frame and stops redrawing.
func (v *liveView) finish() {
	if v == nil {
		return
	}
	close(v.stop)
	<-v.done
	v.render()
}

// render redraws the list in place.
func (v *liveView) render() {
	v.mu.Lock()
	defer v.mu.Unlock()
	width, height := 100, 40
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		width, height = w, h
	}
	lines := v.frame(width, height)

	var b bytes.Buffer
	if v.lines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", v.lines) // back to the first line of the last frame
	}
	for _, l := range lines {
		b.WriteString("\r\x1b[2K")
		b.WriteString(l)
		b.WriteString("\n")
	}
	// Clear lines left over from a taller previous frame.
	for i := len(lines); i < v.lines; i++ {
		b.WriteString("\r\x1b[2K\n")
	}
	if extra := v.lines - len(lines); extra > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", extra)
	}
	v.out.Write(b.Bytes())
	v.lines = len(lines)
}

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiBold  = "\x1b[1m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiCyan  = "\x1b[36m"
)

// frame builds the lines of one frame. Each line fits in width-1 columns so
// the terminal never wraps it (wrapping would break the cursor movement), and
// the frame fits in the window: when it would not, finished tracks are folded
// into one line, then the rest is cut off with a "more" line.
func (v *liveView) frame(width, height int) []string {
	if width < 40 {
		width = 40
	}
	cols := width - 1
	var finished, failed, downloading int
	var rate float64
	now := time.Now()
	for _, it := range v.items {
		switch it.state {
		case liveDone, liveExists:
			finished++
		case liveFailed:
			failed++
		case liveDownloading:
			downloading++
			if el := now.Sub(it.started).Seconds(); el > 0.3 {
				rate += float64(it.done) / el
			}
		}
	}
	header := fmt.Sprintf("%s%s%s · %d/%d done", ansiBold, v.title, ansiReset, finished, len(v.items))
	if failed > 0 {
		header += fmt.Sprintf(" · %s%d failed%s", ansiRed, failed, ansiReset)
	}
	if downloading > 0 {
		header += fmt.Sprintf(" · %d downloading · %.0f Mbit/s", downloading, rate*8/1e6)
	}
	lines := []string{fit(header, cols)}

	shown := v.items
	maxLines := height - 2 // header, and one spare line for the cursor
	if maxLines < 4 {
		maxLines = 4
	}
	if 2*len(shown) > maxLines-1 {
		// Fold finished tracks into a summary line.
		var rest []*liveItem
		var ok int
		for _, it := range v.items {
			switch it.state {
			case liveDone, liveExists:
				ok++
			default:
				rest = append(rest, it) // failures stay visible too
			}
		}
		if ok > 0 {
			lines = append(lines, fit(fmt.Sprintf("     %s✔ %d track(s) done%s", ansiGreen, ok, ansiReset), cols))
		}
		shown = rest
	}
	room := (maxLines - len(lines)) / 2
	more := 0
	if len(shown) > room {
		more = len(shown) - (room - 1)
		shown = shown[:room-1]
	}
	for _, it := range shown {
		lines = append(lines, itemLines(it, cols, now)...)
	}
	if more > 0 {
		lines = append(lines, fit(fmt.Sprintf("     %s… %d more track(s)%s", ansiDim, more, ansiReset), cols))
	}
	return lines
}

const barWidth = 30

func itemLines(it *liveItem, cols int, now time.Time) []string {
	// Line 1: number, title — artist, quality on the right.
	quality := it.quality
	if quality == "" {
		quality = "…"
	}
	left := fmt.Sprintf("%02d. %s", it.num, it.name)
	if it.artist != "" {
		left += " — " + it.artist
	}
	qw := runewidth.StringWidth(quality)
	avail := cols - qw - 2
	if avail < 10 {
		avail = 10
	}
	left = runewidth.Truncate(left, avail, "…")
	pad := cols - runewidth.StringWidth(left) - qw
	if pad < 1 {
		pad = 1
	}
	line1 := left + strings.Repeat(" ", pad) + ansiDim + quality + ansiReset

	// Line 2: bar and status.
	frac := 0.0
	var status string
	color := ""
	switch it.state {
	case liveWaiting:
		status = ansiDim + "waiting" + ansiReset
	case liveDownloading:
		if it.total > 0 {
			frac = float64(it.done) / float64(it.total)
			status = fmt.Sprintf("%3.0f%%  %s/%s", frac*100, mb(it.done), mb(it.total))
		} else {
			status = "starting…"
		}
		if el := now.Sub(it.started).Seconds(); el > 0.3 && it.done > 0 {
			status += fmt.Sprintf("  %.1f MB/s", float64(it.done)/el/1e6)
		}
		color = ansiCyan
	case liveProcessing, liveConverting:
		frac = 1
		status = it.step
		color = ansiCyan
	case liveDone:
		frac = 1
		status = fmt.Sprintf("%s✔ done%s", ansiGreen, ansiReset)
		if it.downloadB > 0 {
			status += fmt.Sprintf(" · %s in %.1f s", mb(it.downloadB), it.finished.Seconds())
		}
		if it.note != "" {
			status += " · " + it.note
		}
		color = ansiGreen
	case liveExists:
		frac = 1
		status = fmt.Sprintf("%s✔ already downloaded%s", ansiGreen, ansiReset)
		color = ansiGreen
	case liveFailed:
		status = fmt.Sprintf("%s✖ %s%s", ansiRed, it.step, ansiReset)
		color = ansiRed
	}
	filled := int(frac*barWidth + 0.5)
	if filled > barWidth {
		filled = barWidth
	}
	bar := color + strings.Repeat("█", filled) + ansiReset + ansiDim + strings.Repeat("░", barWidth-filled) + ansiReset
	line2 := fit("    "+bar+"  "+status, cols)
	return []string{line1, line2}
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }

// fit cuts s to cols visible columns, ignoring ANSI escape sequences.
func fit(s string, cols int) string {
	var b strings.Builder
	w := 0
	cut := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // copy escape sequences, they take no space
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < '@' || s[j] > '~') {
					j++
				}
				j++
			}
			if j > len(s) {
				j = len(s)
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		rn, size := decodeRune(s[i:])
		rw := runewidth.RuneWidth(rn)
		if w+rw > cols {
			cut = true
			i += size
			continue
		}
		if !cut {
			b.WriteString(s[i : i+size])
			w += rw
		}
		i += size
	}
	if cut {
		b.WriteString(ansiReset)
	}
	return b.String()
}

func decodeRune(s string) (rune, int) {
	for _, rn := range s {
		return rn, len(string(rn))
	}
	return 0, 1
}

// formatQuality turns extractMedia's quality ("24B-192.0kHz", "2768 Kbps",
// "256 Kbps") into a label for the list.
func formatQuality(q string, atmos, aac bool) string {
	switch {
	case q == "":
		return ""
	case strings.Contains(q, "kHz"):
		// "24B-192.0kHz"
		parts := strings.SplitN(q, "B-", 2)
		if len(parts) == 2 {
			khz := strings.TrimSuffix(parts[1], "kHz")
			if f, err := strconv.ParseFloat(khz, 64); err == nil {
				return fmt.Sprintf("ALAC %s-bit/%s kHz", parts[0], strconv.FormatFloat(f, 'f', -1, 64))
			}
		}
		return "ALAC " + q
	case atmos:
		return "Atmos " + strings.Replace(q, "Kbps", "kbps", 1)
	case aac:
		return "AAC " + strings.Replace(q, "Kbps", "kbps", 1)
	}
	return q
}

// progressRe matches a frame of the download's progress bar, e.g.
// "Decrypting...  35%  (14/40 MB, 1.4 GB/s)".
var progressRe = regexp.MustCompile(`(\d+)%\s+\(([\d.]+)/([\d.]+) ([kMG]?B)`)

// parseProgress reads the progress bar output of a download worker and calls
// report(done, total) for every frame. Frames are separated by \r or \n.
func parseProgress(r io.Reader, report func(done, total int64)) {
	buf := make([]byte, 4096)
	var frame []byte
	flush := func() {
		if m := progressRe.FindSubmatch(frame); m != nil {
			unit := map[string]float64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9}[string(m[4])]
			d, _ := strconv.ParseFloat(string(m[2]), 64)
			t, _ := strconv.ParseFloat(string(m[3]), 64)
			if unit > 0 && t > 0 {
				report(int64(d*unit), int64(t*unit))
			}
		}
		frame = frame[:0]
	}
	for {
		n, err := r.Read(buf)
		for _, c := range buf[:n] {
			if c == '\r' || c == '\n' {
				flush()
				continue
			}
			if len(frame) < 1024 {
				frame = append(frame, c)
			}
		}
		if err != nil {
			flush()
			return
		}
	}
}

// selectedTracks returns the tracks whose 1-based numbers are in selected.
func selectedTracks(tracks []model.Track, selected []int) []*model.Track {
	var out []*model.Track
	for i := range tracks {
		if contains(selected, i+1) {
			out = append(out, &tracks[i])
		}
	}
	return out
}

// beginLive starts the live list for the tracks about to be downloaded. It
// does nothing when the list cannot be shown, or when music videos are among
// them (their downloader prints its own progress).
func (r *Runner) beginLive(title string, tracks []*model.Track) {
	if r.live != nil {
		return
	}
	items := make([]liveItem, 0, len(tracks))
	for _, t := range tracks {
		if t.Type == "music-videos" {
			return
		}
		items = append(items, liveItem{num: t.TaskNum, name: t.Resp.Attributes.Name, artist: t.Resp.Attributes.ArtistName})
	}
	r.live = r.startLive(title, items)
}

// markLiveDone shows a track finished in an earlier attempt as done.
func (r *Runner) markLiveDone(num int) {
	r.live.update(r.live.item(num), func(it *liveItem) { it.state = liveExists })
}

// endLive waits for every queued track, draws the final list and then prints
// the messages of tracks that failed.
func (r *Runner) endLive() {
	if r.live == nil {
		return
	}
	r.waitJobs()
	v := r.live
	v.finish()
	r.live = nil
	for _, it := range v.items {
		if it.state != liveFailed {
			continue
		}
		fmt.Printf("%s\u2716 %02d. %s%s\n", ansiRed, it.num, it.name, ansiReset)
		for _, n := range it.notes {
			if n = strings.TrimSpace(n); n != "" {
				fmt.Println("   " + n)
			}
		}
	}
}
