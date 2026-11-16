package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"amdl/internal/media/alacfix"
	defrag "amdl/internal/media/defrag"
	"amdl/internal/model"
)

// Background post-processing: once a track is downloaded, its defrag,
// alac-fix, tagging and ffmpeg conversion run on a background worker while
// the main goroutine downloads the next track (network/wrapper-bound).
//
// A track is downloaded to a temporary ".part" name and only renamed to its
// final name once it is fully tagged, so a run that is stopped halfway never
// leaves a file that the next run mistakes for a finished track.
//
// The workers never touch Runner.State or print directly: each job returns its
// messages and outcome, and the main goroutine applies them strictly in the
// order the jobs were queued (so counters, AddedTracks and M3U order stay as
// before), between tracks (collectJobs) or when it needs every job to be done
// (waitJobs), e.g. before writing an M3U playlist or the summary.

// bgWorkers is how many tracks are post-processed at the same time. One job
// takes longer than one download on fast connections (convert alone ~2 s), so
// one worker would fall behind; two keep up.
const bgWorkers = 2

// trackJob is one track's background work. It holds copies of everything it
// needs; track is only read, and the main goroutine no longer changes it.
type trackJob struct {
	seq int

	// Post-processing of a fresh download (post == false: convert only, for an
	// existing .m4a left over from an interrupted run).
	post          bool
	track         *model.Track
	lrc           string
	partPath      string // where the download was written
	finalPath     string // the .m4a name once tagged
	coverToRemove string // per-track playlist cover, deleted after tagging

	convert    bool
	convertJob convertJob

	// Applied by the main goroutine when the job is collected.
	preID   string
	taskNum int
	added   AddedTrack
}

type trackResult struct {
	job     trackJob
	counter string // "", "error" or "unavailable": the track failed
	path    string // the track's final file (converted one if converted)
	output  string
	stages  []stageTime
}

type bgWorker struct {
	jobs     chan trackJob
	pending  sync.WaitGroup
	mu       sync.Mutex
	done     map[int]trackResult
	nextSeq  int             // next job to queue; main goroutine only
	applySeq int             // next job to apply; main goroutine only
	inflight map[string]bool // finalPath of queued jobs; main goroutine only
}

// queueJob hands a job to the background workers, starting them on first use.
func (r *Runner) queueJob(job trackJob) {
	if r.bg == nil {
		w := &bgWorker{
			jobs:     make(chan trackJob, 256),
			done:     make(map[int]trackResult),
			inflight: make(map[string]bool),
		}
		r.bg = w
		for i := 0; i < bgWorkers; i++ {
			go func() {
				for job := range w.jobs {
					res := r.runJob(job)
					w.mu.Lock()
					w.done[job.seq] = res
					w.mu.Unlock()
					w.pending.Done()
				}
			}()
		}
	}
	job.seq = r.bg.nextSeq
	r.bg.nextSeq++
	r.bg.inflight[job.finalPath] = true
	r.bg.pending.Add(1)
	r.bg.jobs <- job
}

// isInflight reports whether a queued job is still producing this file, e.g.
// when the same song appears twice in a playlist.
func (r *Runner) isInflight(finalPath string) bool {
	return r.bg != nil && r.bg.inflight[finalPath]
}

// runJob does one job's work. It runs on a worker goroutine: it only reads
// r.Config and the job, and reports everything through its result.
func (r *Runner) runJob(job trackJob) trackResult {
	var out bytes.Buffer
	res := trackResult{job: job, path: job.finalPath}
	last := time.Now()
	mark := func(name string) {
		now := time.Now()
		res.stages = append(res.stages, stageTime{name, now.Sub(last)})
		last = now
	}
	fail := func(counter string, format string, args ...any) trackResult {
		fmt.Fprintf(&out, format+"\n", args...)
		// Drop the unfinished file, so a retry downloads the track again.
		if job.post {
			os.Remove(job.partPath)
		}
		res.counter = counter
		res.output = out.String()
		return res
	}

	if job.post {
		if err := defrag.DefragmentMP4(job.partPath); err != nil {
			return fail("error", "Defragment failed: %v", err)
		}
		mark("defrag")

		if r.Config.Media.ALACFix {
			fixed, err := alacfix.Fix(job.partPath, false)
			if err != nil {
				return fail("unavailable", "⚠ Failed to fix ALAC: %v", err)
			}
			if fixed.TracksCount > 0 {
				if fixed.Patched > 0 {
					word := "packets"
					if fixed.Patched == 1 {
						word = "packet"
					}
					fmt.Fprintf(&out, "Repaired %d affected %s\n", fixed.Patched, word)
				} else {
					fmt.Fprintln(&out, "Audio integrity intact")
				}
			}
			mark("alac-fix")
		}

		if err := r.writeMP4Tags(job.track, job.partPath, job.lrc); err != nil {
			return fail("unavailable", "⚠ Failed to write tags in media: %v", err)
		}
		if err := os.Rename(job.partPath, job.finalPath); err != nil {
			return fail("error", "Failed to rename %s: %v", filepath.Base(job.partPath), err)
		}
		mark("tags")

		if job.coverToRemove != "" {
			if err := os.Remove(job.coverToRemove); err != nil {
				fmt.Fprintf(&out, "Error deleting file: %s\n", job.coverToRemove)
				res.counter = "error"
				res.output = out.String()
				return res
			}
		}
	}

	if job.convert {
		if outPath, ok := r.runConvert(job.convertJob, &out); ok {
			res.path = outPath
		}
		mark("convert")
	}
	res.output = out.String()
	return res
}

// collectJobs applies finished jobs, in queue order, without waiting for the
// rest: prints their messages and updates counters and AddedTracks.
func (r *Runner) collectJobs() {
	if r.bg == nil {
		return
	}
	var ready []trackResult
	r.bg.mu.Lock()
	for {
		res, ok := r.bg.done[r.bg.applySeq]
		if !ok {
			break
		}
		delete(r.bg.done, r.bg.applySeq)
		r.bg.applySeq++
		ready = append(ready, res)
	}
	r.bg.mu.Unlock()

	for _, res := range ready {
		r.applyJob(res)
	}
}

func (r *Runner) applyJob(res trackResult) {
	job := res.job
	delete(r.bg.inflight, job.finalPath)
	fmt.Printf("[background] %s\n", filepath.Base(job.finalPath))
	fmt.Print(res.output)
	for _, s := range res.stages {
		r.addBackgroundTiming(s.name, s.d)
	}
	switch res.counter {
	case "error":
		r.State.Counter.Error++
		return
	case "unavailable":
		r.State.Counter.Unavailable++
		return
	}
	if job.post {
		r.State.Counter.Success++
		r.State.OKDict[job.preID] = append(r.State.OKDict[job.preID], job.taskNum)
	}
	added := job.added
	added.Path = res.path
	r.State.AddedTracks = append(r.State.AddedTracks, added)
}

// waitJobs blocks until every queued job is done, then applies them all.
func (r *Runner) waitJobs() {
	if r.bg == nil {
		return
	}
	start := time.Now()
	r.bg.pending.Wait()
	r.collectJobs()
	r.addWaitTiming(time.Since(start))
}
