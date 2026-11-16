package app

import (
	"testing"
	"time"
)

func TestStageTimerOffIsNil(t *testing.T) {
	r := &Runner{}
	timer := r.newStageTimer()
	if timer != nil {
		t.Fatal("expected nil timer when --timings is off")
	}
	// A nil timer must be safe to use.
	timer.mark("download")
	r.finishStageTimer(timer)
	if r.State.Timings.tracks != 0 {
		t.Fatal("nil timer must not count tracks")
	}
}

func TestStageTimerTotals(t *testing.T) {
	r := &Runner{Flags: Flags{Timings: true}}
	for i := 0; i < 2; i++ {
		timer := r.newStageTimer()
		time.Sleep(2 * time.Millisecond)
		timer.mark("download")
		timer.setBytes(1_000_000)
		timer.mark("tags")
		r.finishStageTimer(timer)
	}
	if r.State.Timings.bytes != 2_000_000 {
		t.Fatalf("bytes = %d, want 2000000", r.State.Timings.bytes)
	}
	tm := r.State.Timings
	if tm.tracks != 2 {
		t.Fatalf("tracks = %d, want 2", tm.tracks)
	}
	if len(tm.order) != 2 || tm.order[0] != "download" || tm.order[1] != "tags" {
		t.Fatalf("order = %v", tm.order)
	}
	if tm.totals["download"] < 4*time.Millisecond {
		t.Fatalf("download total = %v, want >= 4ms", tm.totals["download"])
	}
	if sum := tm.totals["download"] + tm.totals["tags"]; sum != tm.total {
		t.Fatalf("stages sum %v != total %v", sum, tm.total)
	}
	r.printTimingsSummary()
}

func TestSpeed(t *testing.T) {
	if got, want := speed(50_000_000, 2*time.Second), "50.0 MB at 25.0 MB/s = 200 Mbit/s"; got != want {
		t.Errorf("speed = %q, want %q", got, want)
	}
	if speed(0, time.Second) != "" || speed(1, 0) != "" {
		t.Error("speed must be empty without bytes or time")
	}
}
