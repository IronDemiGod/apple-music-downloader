package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// Live progress for coverfix: finished albums print their usual result lines,
// which scroll up as a log; below them, redrawn in place, an overall bar and
// one line per album being processed (at most --parallel lines, however many
// albums there are):
//
//	✔ ADO/Vivarium - Single: cover.png, embedded in 1/1 tracks (album found by UPC)
//	── coverfix · 37/412 albums · 3 failed ──────────────
//	████████░░░░░░░░░░░░░░░░░░░░░░   9%   ~6 min left
//	YOASOBI/THE BOOK 3      ██████████████░░░░░░   9/14 tracks  embedding
//	Eve/Under Blue          ░░░░░░░░░░░░░░░░░░░░   downloading the cover

type coverAlbum struct {
	name        string
	step        string
	done, total int // tracks embedded / to embed
}

type coverView struct {
	out   io.Writer
	total int
	start time.Time

	mu       sync.Mutex
	finished int
	failed   int
	active   map[int]*coverAlbum
	logs     []string // finished albums' lines, printed above the bars
	lines    int      // lines of the last drawn frame
	last     string

	stop, done chan struct{}
}

// albumProgress reports one album's progress to the view; a nil
// *albumProgress (no view) does nothing.
type albumProgress struct {
	v *coverView
	i int
}

func (p *albumProgress) step(s string) {
	if p == nil {
		return
	}
	p.v.mu.Lock()
	if a := p.v.active[p.i]; a != nil {
		a.step = s
	}
	p.v.mu.Unlock()
}

func (p *albumProgress) tracks(done, total int) {
	if p == nil {
		return
	}
	p.v.mu.Lock()
	if a := p.v.active[p.i]; a != nil {
		a.done, a.total = done, total
	}
	p.v.mu.Unlock()
}

// newCoverView starts the live display, or returns nil when it cannot be
// shown (not a terminal, --no-live).
func (r *Runner) newCoverView(total int) *coverView {
	if total == 0 || r.Flags.NoLive || !term.IsTerminal(int(os.Stdout.Fd())) || !enableVT() {
		return nil
	}
	v := &coverView{out: os.Stdout, total: total, start: time.Now(), active: map[int]*coverAlbum{},
		stop: make(chan struct{}), done: make(chan struct{})}
	io.WriteString(v.out, "\x1b[?25l")
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

// begin shows album i as being processed.
func (v *coverView) begin(i int, name string) *albumProgress {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	v.active[i] = &coverAlbum{name: name, step: "reading tracks"}
	v.mu.Unlock()
	return &albumProgress{v, i}
}

// end removes album i from the bars and counts it.
func (v *coverView) end(i int, failed bool) {
	if v == nil {
		return
	}
	v.mu.Lock()
	delete(v.active, i)
	v.finished++
	if failed {
		v.failed++
	}
	v.mu.Unlock()
}

// log queues an album's result lines, printed above the bars at the next
// redraw.
func (v *coverView) log(out string) {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return
	}
	v.mu.Lock()
	v.logs = append(v.logs, strings.Split(out, "\n")...)
	v.mu.Unlock()
}

// finish draws the final state (header and overall bar) and leaves it on
// screen.
func (v *coverView) finish() {
	if v == nil {
		return
	}
	close(v.stop)
	<-v.done
	v.render()
	io.WriteString(v.out, "\x1b[?25h")
}

func (v *coverView) moveToTop(b *bytes.Buffer) {
	if v.lines > 0 {
		fmt.Fprintf(b, "\r\x1b[%dA", v.lines)
	}
}

func (v *coverView) render() {
	v.mu.Lock()
	defer v.mu.Unlock()
	width := 100
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		width = w
	}
	frame := v.frame(width)
	key := strings.Join(frame, "\n")
	if len(v.logs) == 0 && key == v.last && len(frame) == v.lines {
		return
	}
	v.last = key

	var b bytes.Buffer
	b.WriteString("\x1b[?2026h")
	v.moveToTop(&b)
	if len(v.logs) > 0 {
		// New log lines go where the bars were; the bars follow them.
		b.WriteString("\x1b[J")
		for _, l := range v.logs {
			b.WriteString(l + "\n")
		}
		v.logs = nil
		for _, l := range frame {
			b.WriteString(l + "\x1b[K\n")
		}
	} else {
		for _, l := range frame {
			b.WriteString("\r" + l + "\x1b[K\n")
		}
		for i := len(frame); i < v.lines; i++ {
			b.WriteString("\r\x1b[K\n")
		}
		if extra := v.lines - len(frame); extra > 0 {
			fmt.Fprintf(&b, "\x1b[%dA", extra)
		}
	}
	b.WriteString("\x1b[?2026l")
	v.out.Write(b.Bytes())
	v.lines = len(frame)
}

// frame builds the bar lines; each fits in width-1 columns.
func (v *coverView) frame(width int) []string {
	if width < 40 {
		width = 40
	}
	cols := width - 1

	head := fmt.Sprintf("── coverfix · %d/%d albums", v.finished, v.total)
	if v.failed > 0 {
		head += fmt.Sprintf(" · %s%d failed%s", ansiRed, v.failed, ansiReset)
	}
	head += " "
	if pad := cols - runewidth.StringWidth(visibleText(head)); pad > 0 {
		head += strings.Repeat("─", pad)
	}
	lines := []string{fit(ansiBold+head+ansiReset, cols)}

	frac := 0.0
	if v.total > 0 {
		frac = float64(v.finished) / float64(v.total)
	}
	overall := bar(frac, barWidth, ansiCyan) + fmt.Sprintf("  %3.0f%%", frac*100)
	if v.finished > 0 && v.finished < v.total {
		left := time.Duration(float64(time.Since(v.start)) / float64(v.finished) * float64(v.total-v.finished))
		overall += "   ~" + roughDur(left) + " left"
	}
	lines = append(lines, fit(overall, cols))

	idx := make([]int, 0, len(v.active))
	for i := range v.active {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	nameW := cols / 3
	if nameW < 16 {
		nameW = 16
	}
	for _, i := range idx {
		a := v.active[i]
		name := runewidth.FillRight(runewidth.Truncate(a.name, nameW, "…"), nameW)
		f := 0.0
		status := a.step
		if a.total > 0 {
			f = float64(a.done) / float64(a.total)
			status = fmt.Sprintf("%d/%d tracks  %s", a.done, a.total, a.step)
		}
		lines = append(lines, fit(name+"  "+bar(f, 20, ansiCyan)+"  "+status, cols))
	}
	return lines
}

func bar(frac float64, w int, color string) string {
	n := int(frac*float64(w) + 0.5)
	if n > w {
		n = w
	}
	return color + strings.Repeat("█", n) + ansiReset + ansiDim + strings.Repeat("░", w-n) + ansiReset
}

func visibleText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && (s[i] < '@' || s[i] > '~' || s[i] == '[') {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func roughDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds())+1)
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}
