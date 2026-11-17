package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"amdl/internal/model"
)

// Live progress list, in the same style as coverfix's: while an album or
// playlist downloads, a header with the song count, an overall bar, and one
// line per track being downloaded or processed, redrawn in place:
//
//	── ADO — Vivarium · 3/12 songs · 3 downloading · 540 Mbit/s ─────────
//	████████░░░░░░░░░░░░░░░░░░░░░░   27%   ~40 s left
//	04. Song name — ADO   ██████████░░░░░░░░░░   52%  33.1 MB/63.8 MB  17.2 MB/s  ALAC 24-bit/96 kHz
//	03. Other song — ADO  ████████████████████   converting to FLAC  ALAC 24-bit/96 kHz
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
	id      string // track ID
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
	claimed   bool          // taken by a track (see itemFor)
	endedAt   time.Time     // when the list first drew it finished (done, skipped, failed)
}

// recentFor is how long a finished track keeps its line, so tracks that are
// skipped (already downloaded) or finish quickly are seen too; at most
// maxRecent of them (the newest) are shown at once.
const (
	recentFor = 3 * time.Second
	maxRecent = 3
)

type liveView struct {
	title string
	out   io.Writer // the terminal
	fd    int       // its file descriptor, for the window size
	start time.Time

	// While the list is shown, os.Stdout is a pipe: anything else printed
	// (amdl's own messages) is shown as lines above the bars, as coverfix
	// does, instead of breaking the redraw.
	realStdout *os.File
	pipeW      *os.File
	readDone   chan struct{}
	logs       []string // captured lines not yet drawn
	partial    []byte   // a captured line without its newline yet

	mu    sync.Mutex
	items []*liveItem
	lines int    // lines drawn by the last frame
	last  string // the last frame drawn, to skip unchanged redraws

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
	v := &liveView{title: title, out: os.Stdout, fd: int(os.Stdout.Fd()), start: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	for i := range tracks {
		it := tracks[i]
		v.items = append(v.items, &it)
	}
	io.WriteString(v.out, "\x1b[?25l") // hide the cursor while redrawing
	v.captureStdout()
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

// captureStdout points os.Stdout at a pipe whose lines are shown above the
// bars. Without it the list is still drawn, but stray prints break it.
func (v *liveView) captureStdout() {
	pr, pw, err := os.Pipe()
	if err != nil {
		return
	}
	v.realStdout, v.pipeW = os.Stdout, pw
	v.readDone = make(chan struct{})
	os.Stdout = pw
	go func() {
		defer close(v.readDone)
		defer pr.Close()
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				v.addOutput(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
}

// addOutput splits captured output into lines for the log above the bars.
func (v *liveView) addOutput(p []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, c := range p {
		switch c {
		case '\n':
			v.logs = append(v.logs, strings.TrimRight(string(v.partial), "\r"))
			v.partial = v.partial[:0]
		default:
			if len(v.partial) < 4096 {
				v.partial = append(v.partial, c)
			}
		}
	}
}

// releaseStdout gives os.Stdout back and keeps what was still in the pipe.
func (v *liveView) releaseStdout() {
	if v.pipeW == nil {
		return
	}
	os.Stdout = v.realStdout
	v.pipeW.Close()
	<-v.readDone
	v.mu.Lock()
	if len(v.partial) > 0 {
		v.logs = append(v.logs, string(v.partial))
		v.partial = nil
	}
	v.mu.Unlock()
	v.pipeW = nil
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

// itemFor returns the list entry for a track about to be downloaded and
// claims it: the first unclaimed entry with the track's ID, else with its
// number. By ID, a playlist's list finds its tracks while they are downloaded
// as part of their albums (with the albums' track numbers); claiming keeps a
// song listed twice on two entries.
func (v *liveView) itemFor(t *model.Track) *liveItem {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var found *liveItem
	for _, it := range v.items {
		if !it.claimed && t.ID != "" && it.id == t.ID {
			found = it
			break
		}
	}
	if found == nil {
		for _, it := range v.items {
			if !it.claimed && it.id == "" && it.num == t.TaskNum {
				found = it
				break
			}
		}
	}
	if found != nil {
		found.claimed = true
	}
	return found
}

// failItem shows an unclaimed entry of the track ID as failed with reason.
func (v *liveView) failItem(id, reason string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, it := range v.items {
		if !it.claimed && it.id == id {
			it.claimed = true
			it.state, it.step = liveFailed, reason
			it.notes = append(it.notes, reason)
			return
		}
	}
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
	v.releaseStdout()
	v.render()
	io.WriteString(v.out, "\x1b[?25h") // show the cursor again
}

// render redraws the list in place.
func (v *liveView) render() {
	v.mu.Lock()
	defer v.mu.Unlock()
	width, height := 100, 40
	if w, h, err := term.GetSize(v.fd); err == nil {
		width, height = w, h
	}
	lines := v.frame(width, height)
	key := strings.Join(lines, "\n")
	if len(v.logs) == 0 && key == v.last && v.lines == len(lines) {
		return // nothing changed
	}
	v.last = key

	// One write per frame, wrapped in "synchronized output" (mode 2026) so
	// terminals that support it (Windows Terminal, most modern ones) show the
	// frame only once it is complete; others ignore it. Each line is
	// overwritten and then erased to its end, never blanked first, so a frame
	// that arrives in pieces (e.g. over SSH) does not flash empty lines.
	var b bytes.Buffer
	b.WriteString("\x1b[?2026h")
	if v.lines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", v.lines) // back to the first line of the last frame
	}
	// Captured lines go where the bars were, scrolling up as a log; the
	// bars follow them.
	for _, l := range v.logs {
		b.WriteString("\r" + l + "\x1b[K\n")
	}
	written := len(v.logs)
	v.logs = nil
	for _, l := range lines {
		b.WriteString("\r")
		b.WriteString(l)
		b.WriteString("\x1b[K\n")
	}
	// Clear lines left over from a taller previous frame.
	written += len(lines)
	for i := written; i < v.lines; i++ {
		b.WriteString("\r\x1b[K\n")
	}
	if extra := v.lines - written; extra > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", extra)
	}
	b.WriteString("\x1b[?2026l")
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

// frame builds the lines of one frame, in coverfix's style: a header with the
// song count, an overall bar, then one line per track being downloaded or
// processed (waiting and finished tracks are not listed). Each line fits in
// width-1 columns so the terminal never wraps it (wrapping would break the
// cursor movement), and the frame fits in the window.
func (v *liveView) frame(width, height int) []string {
	if width < 40 {
		width = 40
	}
	cols := width - 1
	now := time.Now()
	var finished, failed, downloading, existed int
	var rate, progress float64
	var active, recent []*liveItem
	for _, it := range v.items {
		switch it.state {
		case liveDone, liveExists, liveFailed:
			if it.endedAt.IsZero() {
				it.endedAt = now
			}
			if now.Sub(it.endedAt) < recentFor {
				recent = append(recent, it)
			}
		}
		switch it.state {
		case liveDone:
			finished++
			progress++
		case liveExists:
			finished++
			existed++
			progress++
		case liveFailed:
			failed++
			progress++
		case liveDownloading:
			downloading++
			if it.total > 0 {
				f := float64(it.done) / float64(it.total)
				if f > 1 {
					f = 1
				}
				progress += 0.9 * f // the rest is processing
			}
			if el := now.Sub(it.started).Seconds(); el > 0.3 {
				rate += float64(it.done) / el
			}
			active = append(active, it)
		case liveProcessing, liveConverting:
			progress += 0.9
			active = append(active, it)
		}
	}
	total := len(v.items)

	title := runewidth.Truncate(v.title, cols/2, "…")
	head := fmt.Sprintf("── %s · %d/%d songs", title, finished, total)
	if failed > 0 {
		head += fmt.Sprintf(" · %s%d failed%s%s", ansiRed, failed, ansiReset, ansiBold)
	}
	if downloading > 0 {
		head += fmt.Sprintf(" · %d downloading · %.0f Mbit/s", downloading, rate*8/1e6)
	}
	head += " "
	if pad := cols - runewidth.StringWidth(visibleText(head)); pad > 0 {
		head += strings.Repeat("─", pad)
	}
	lines := []string{fit(ansiBold+head+ansiReset, cols)}

	frac := 0.0
	if total > 0 {
		frac = progress / float64(total)
	}
	overall := bar(frac, barWidth, ansiCyan) + fmt.Sprintf("  %3.0f%%", frac*100)
	// Time left from the progress made in this run (tracks that already
	// existed took no time).
	if gained := progress - float64(existed); gained > 0.2 && frac < 1 && !v.start.IsZero() {
		left := time.Duration(float64(now.Sub(v.start)) / gained * (float64(total) - progress))
		overall += "   ~" + roughDur(left) + " left"
	}
	lines = append(lines, fit(overall, cols))

	room := height - 1 - len(lines) // one spare line for the cursor
	if room < 2 {
		room = 2
	}
	more := 0
	if len(active) > room {
		more = len(active) - (room - 1)
		active = active[:room-1]
		recent = nil
	}
	// Finished tracks take the room the active ones leave, newest first, at
	// most maxRecent.
	free := room - len(active)
	if free > maxRecent {
		free = maxRecent
	}
	if free < 0 {
		free = 0
	}
	if len(recent) > free {
		sort.SliceStable(recent, func(i, j int) bool { return recent[i].endedAt.After(recent[j].endedAt) })
		recent = recent[:free]
	}
	shown := append(active, recent...)
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].num < shown[j].num })
	lines = append(lines, itemLines(shown, cols, now)...)
	if more > 0 {
		lines = append(lines, fit(fmt.Sprintf("%s… %d more%s", ansiDim, more, ansiReset), cols))
	}
	return lines
}

const barWidth = 30

// itemStatus is the bar fill and status text of a track being downloaded or
// processed.
func itemStatus(it *liveItem, now time.Time) (float64, string, string) {
	f := 0.0
	var status string
	color := ansiCyan
	switch it.state {
	case liveDone:
		f, color = 1, ansiGreen
		status = ansiGreen + "✔ done" + ansiReset
		if it.downloadB > 0 {
			status += fmt.Sprintf(" · %s in %.1f s", mb(it.downloadB), it.finished.Seconds())
		}
		if it.note != "" {
			status += " · " + it.note
		}
	case liveExists:
		f, color = 1, ansiGreen
		status = ansiGreen + "✔ already downloaded" + ansiReset
	case liveFailed:
		color = ansiRed
		status = ansiRed + "✖ " + it.step + ansiReset
	case liveDownloading:
		if it.total > 0 {
			f = float64(it.done) / float64(it.total)
			status = fmt.Sprintf("%3.0f%%  %s/%s", f*100, mb(it.done), mb(it.total))
		} else {
			status = "starting…"
		}
		if el := now.Sub(it.started).Seconds(); el > 0.3 && it.done > 0 {
			status += fmt.Sprintf("  %.1f MB/s", float64(it.done)/el/1e6)
		}
	default: // processing, converting
		f = 1
		status = it.step
	}
	if it.quality != "" {
		status += "  " + ansiDim + it.quality + ansiReset
	}
	return f, status, color
}

// itemLines draws the active tracks, one line each, with the names in one
// column as wide as the longest status leaves room for (at most 2/5 of the
// line), so the bars line up:
//
//  02. Vivarium — ADO   ██████████░░░░░░░░░░   52%  33.1 MB/63.8 MB  17.2 MB/s  ALAC 24-bit/96 kHz
func itemLines(items []*liveItem, cols int, now time.Time) []string {
	fracs := make([]float64, len(items))
	statuses := make([]string, len(items))
	colors := make([]string, len(items))
	widest := 0
	for i, it := range items {
		fracs[i], statuses[i], colors[i] = itemStatus(it, now)
		if w := runewidth.StringWidth(visibleText(statuses[i])); w > widest {
			widest = w
		}
	}
	// A long status (a failure reason) is cut at the line's end rather
	// than squeezing the names.
	if maxStatus := cols / 2; widest > maxStatus {
		widest = maxStatus
	}
	nameW := cols - 24 - widest
	if maxW := cols * 2 / 5; nameW > maxW {
		nameW = maxW
	}
	if nameW < 16 {
		nameW = 16
	}
	lines := make([]string, len(items))
	for i, it := range items {
		label := fmt.Sprintf("%02d. %s", it.num, it.name)
		if it.artist != "" {
			label += " — " + it.artist
		}
		label = runewidth.FillRight(runewidth.Truncate(label, nameW, "…"), nameW)
		lines[i] = fit(label+"  "+bar(fracs[i], 20, colors[i])+"  "+statuses[i], cols)
	}
	return lines
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
		items = append(items, liveItem{id: t.ID, num: t.TaskNum, name: t.Resp.Attributes.Name, artist: t.Resp.Attributes.ArtistName})
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
	if r.live == nil || r.liveHeld {
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
