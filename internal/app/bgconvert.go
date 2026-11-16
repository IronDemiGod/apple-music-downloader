package app

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Background conversion: ffmpeg (CPU-bound) runs on one worker goroutine while
// the main goroutine downloads the next track (network/wrapper-bound), which
// hides most of the conversion time.
//
// The worker never touches Runner.State or prints directly: it collects each
// job's messages and result, and the main goroutine applies them between
// tracks (collectConversions) or when it needs every conversion to be done
// (waitConversions), e.g. before writing an M3U playlist or the summary.

type convertResult struct {
	job     convertJob
	outPath string
	ok      bool
	output  string
	d       time.Duration
}

type bgConverter struct {
	jobs    chan convertJob
	pending sync.WaitGroup
	mu      sync.Mutex
	done    []convertResult
	queued  map[string]bool // source paths queued this run; main goroutine only
}

// queueConvert hands a prepared job to the background worker, starting it on
// first use. A file already queued in this run is not queued again (e.g. the
// same song twice in a playlist); it then returns false.
func (r *Runner) queueConvert(job convertJob) bool {
	if r.conv == nil {
		c := &bgConverter{jobs: make(chan convertJob, 256), queued: make(map[string]bool)}
		r.conv = c
		go func() {
			for job := range c.jobs {
				var out bytes.Buffer
				start := time.Now()
				outPath, ok := r.runConvert(job, &out)
				res := convertResult{job: job, outPath: outPath, ok: ok, output: out.String(), d: time.Since(start)}
				c.mu.Lock()
				c.done = append(c.done, res)
				c.mu.Unlock()
				c.pending.Done()
			}
		}()
	}
	if r.conv.queued[job.srcPath] {
		return false
	}
	r.conv.queued[job.srcPath] = true
	r.conv.pending.Add(1)
	r.conv.jobs <- job
	return true
}

// collectConversions applies finished conversions without waiting for the
// rest: prints their messages and fixes their paths in AddedTracks.
func (r *Runner) collectConversions() {
	if r.conv == nil {
		return
	}
	r.conv.mu.Lock()
	done := r.conv.done
	r.conv.done = nil
	r.conv.mu.Unlock()
	for _, res := range done {
		name := res.job.srcPath
		if i := strings.LastIndexAny(name, `/\`); i >= 0 {
			name = name[i+1:]
		}
		fmt.Printf("[background] %s\n", name)
		fmt.Print(res.output)
		if res.ok && res.job.addedIdx >= 0 && res.job.addedIdx < len(r.State.AddedTracks) {
			r.State.AddedTracks[res.job.addedIdx].Path = res.outPath
		}
		r.addBackgroundTiming("convert", res.d)
	}
}

// waitConversions blocks until every queued conversion is done, then applies
// them all.
func (r *Runner) waitConversions() {
	if r.conv == nil {
		return
	}
	start := time.Now()
	r.conv.pending.Wait()
	r.collectConversions()
	r.addWaitTiming(time.Since(start))
}
