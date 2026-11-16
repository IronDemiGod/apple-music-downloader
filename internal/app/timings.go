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
}

// newStageTimer starts timing a track, or returns nil when --timings is off.
func (r *Runner) newStageTimer() *stageTimer {
	if !r.Flags.Timings {
		return nil
	}
	now := time.Now()
	return &stageTimer{start: now, last: now}
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
}

func fmtDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
