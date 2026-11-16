package app

import (
	"fmt"
	"strings"
	"time"
)

// Per-stage timings, printed with --timings. They show where the time of a
// download goes (network/decryption, MP4 rewrites, tagging, ffmpeg), so speed
// work can target the stage that actually dominates.

// stageTimer measures consecutive stages of one track. A nil *stageTimer is
// valid and does nothing, so call sites need no checks when timings are off.
type stageTimer struct {
	start  time.Time
	last   time.Time
	stages []stageTime
	bytes  int64 // size of the downloaded file, for the download speed
}

type stageTime struct {
	name string
	d    time.Duration
}

// Timings accumulates stage durations over every timed track of a run.
type Timings struct {
	order  []string
	totals map[string]time.Duration
	tracks int
	total  time.Duration
	start  time.Time // when the first timed track started (for wall time)
	bytes  int64     // downloaded bytes over all timed tracks

	// Work done on the background converter, overlapping the stages above,
	// and how long the main goroutine waited for it.
	bgOrder  []string
	bgTotals map[string]time.Duration
	bgCount  map[string]int
	wait     time.Duration
}

// newStageTimer starts timing a track, or returns nil when --timings is off.
func (r *Runner) newStageTimer() *stageTimer {
	if !r.Flags.Timings {
		return nil
	}
	now := time.Now()
	if r.State.Timings.start.IsZero() {
		r.State.Timings.start = now
	}
	return &stageTimer{start: now, last: now}
}

// addBackgroundTiming records work done on the background converter.
func (r *Runner) addBackgroundTiming(name string, d time.Duration) {
	if !r.Flags.Timings {
		return
	}
	tm := &r.State.Timings
	if tm.bgTotals == nil {
		tm.bgTotals = make(map[string]time.Duration)
		tm.bgCount = make(map[string]int)
	}
	if _, ok := tm.bgTotals[name]; !ok {
		tm.bgOrder = append(tm.bgOrder, name)
	}
	tm.bgTotals[name] += d
	tm.bgCount[name]++
}

// addWaitTiming records time spent waiting for background work to finish.
func (r *Runner) addWaitTiming(d time.Duration) {
	if !r.Flags.Timings {
		return
	}
	r.State.Timings.wait += d
}

// setBytes records how many bytes the track's download produced.
func (t *stageTimer) setBytes(n int64) {
	if t == nil {
		return
	}
	t.bytes = n
}

// speed formats n bytes transferred in d as MB and MB/s plus Mbit/s.
func speed(n int64, d time.Duration) string {
	if n <= 0 || d <= 0 {
		return ""
	}
	mb := float64(n) / 1e6
	mbps := mb / d.Seconds()
	return fmt.Sprintf("%.1f MB at %.1f MB/s = %.0f Mbit/s", mb, mbps, mbps*8)
}

// stageDur returns the duration of the named stage, or 0.
func (t *stageTimer) stageDur(name string) time.Duration {
	for _, s := range t.stages {
		if s.name == name {
			return s.d
		}
	}
	return 0
}

// mark ends the current stage under the given name and starts the next one.
func (t *stageTimer) mark(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.stages = append(t.stages, stageTime{name, now.Sub(t.last)})
	t.last = now
}

// finish prints the track's stages and adds them to the run totals.
func (r *Runner) finishStageTimer(t *stageTimer) {
	if t == nil {
		return
	}
	total := t.last.Sub(t.start)
	parts := make([]string, 0, len(t.stages)+1)
	for _, s := range t.stages {
		parts = append(parts, fmt.Sprintf("%s %s", s.name, fmtDur(s.d)))
	}
	parts = append(parts, "total "+fmtDur(total))
	if sp := speed(t.bytes, t.stageDur("download")); sp != "" {
		parts = append(parts, sp)
	}
	fmt.Println("\u23F1 " + strings.Join(parts, " | "))

	tm := &r.State.Timings
	if tm.totals == nil {
		tm.totals = make(map[string]time.Duration)
	}
	for _, s := range t.stages {
		if _, ok := tm.totals[s.name]; !ok {
			tm.order = append(tm.order, s.name)
		}
		tm.totals[s.name] += s.d
	}
	tm.tracks++
	tm.total += total
	tm.bytes += t.bytes
}

// printTimingsSummary prints each stage's share of the run's timed tracks.
func (r *Runner) printTimingsSummary() {
	tm := r.State.Timings
	if !r.Flags.Timings || tm.tracks == 0 {
		return
	}
	fmt.Printf("\u23F1 Timings over %d downloaded track(s), total %s:\n", tm.tracks, fmtDur(tm.total))
	for _, name := range tm.order {
		d := tm.totals[name]
		pct := 0.0
		if tm.total > 0 {
			pct = 100 * float64(d) / float64(tm.total)
		}
		fmt.Printf("   %-10s %9s  %5.1f%%  (avg %s)\n", name, fmtDur(d), pct, fmtDur(d/time.Duration(tm.tracks)))
	}
	if sp := speed(tm.bytes, tm.totals["download"]); sp != "" {
		fmt.Printf("   downloaded %s (average while downloading)\n", sp)
	}
	if len(tm.bgOrder) > 0 {
		fmt.Println("   in the background, overlapping the stages above:")
		for _, name := range tm.bgOrder {
			d := tm.bgTotals[name]
			fmt.Printf("   %-10s %9s  (%d file(s), avg %s)\n", name, fmtDur(d), tm.bgCount[name], fmtDur(d/time.Duration(tm.bgCount[name])))
		}
		fmt.Printf("   waited for the background work: %s\n", fmtDur(tm.wait))
	}
	if !tm.start.IsZero() {
		fmt.Printf("   wall time: %s\n", fmtDur(time.Since(tm.start)))
	}
}

func fmtDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
