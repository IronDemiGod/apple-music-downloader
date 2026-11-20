package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"amdl/internal/media/alacfix"
	defrag "amdl/internal/media/defrag"
	"amdl/internal/media/lyrics"
	"amdl/internal/model"

	"amdl/internal/fsutil"
)

// Background work. The main goroutine walks the tracks in order (names,
// "already exists" checks) and queues each new track as a job; workers then do
// the rest while the main goroutine moves on:
//
//   - with general.parallel-downloads > 1: the download itself (in a child
//     process, see dlworker.go) plus the lyrics, up to that many at once;
//   - always: defrag, alac-fix, tagging, the rename from the temporary ".part"
//     name to the final name, and the optional ffmpeg conversion.
//
// A track is downloaded to a ".part" name and only renamed to its final name
// once it is fully tagged, so a run that is stopped halfway never leaves a file
// that the next run mistakes for a finished track.
//
// The workers never touch Runner.State or print directly: each job returns its
// messages and outcome, and the main goroutine applies them strictly in the
// order the jobs were queued (so counters, AddedTracks, M3U order and output
// stay in sequence), between tracks (collectJobs) or while waiting for all of
// them (waitJobs), e.g. before writing an M3U playlist or the summary.

const (
	// postWorkers is how many tracks are post-processed at the same time when
	// downloads run one after another. One job can take longer than one
	// download on a fast connection (convert alone ~2 s); two keep up.
	postWorkers = 2
)

// autoConverts is how many ffmpeg conversions run at once by default.
// ffmpeg's FLAC encoder uses one core per file, so a machine with more cores
// converts more files at the same time: one per three CPU threads, between 2
// and 6 (2 on a 2-core/4-thread laptop, 4 on a 12-thread PC). The settings,
// and so the output, of each conversion are unchanged.
var autoConverts = func() int {
	n := runtime.NumCPU() / 3
	if n < 2 {
		n = 2
	}
	if n > 6 {
		n = 6
	}
	return n
}()

// parallelConverts is how many conversions run at once: convert.parallel,
// or autoConverts when it is 0.
func (r *Runner) parallelConverts() int {
	n := r.Config.Convert.Parallel
	switch {
	case n <= 0:
		return autoConverts
	case n > 32:
		return 32
	}
	return n
}

// trackJob is one track's background work. It holds copies of everything it
// needs; track is only read, and the main goroutine no longer changes it.
type trackJob struct {
	seq int

	// download: fetch the track first (parallel downloads only).
	download    bool
	aacLC       bool   // download through the lite-server aac-lc path
	lyrics      bool   // fetch lyrics alongside the download
	lrcFilename string // where to save lyrics when lyrics.save-file is on

	// Post-processing of a fresh download (post == false: convert only, for an
	// existing .m4a left over from an interrupted run).
	post          bool
	track         *model.Track
	lrc           string
	partPath      string        // where the download is written
	finalPath     string        // the .m4a name once tagged
	coverToRemove string        // per-track playlist cover, deleted after tagging
	cover         *pendingCover // album cover still downloading; waited for at tagging
	// workDir: the track's work folder (paths.work-dir), or "" to work in
	// the save folder. Everything finished in it is moved there at the end.
	workDir string

	// Live progress list entry (nil when the list is not shown).
	view *liveView
	item *liveItem

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
	bytes   int64 // downloaded bytes (download jobs)
}

type bgWorker struct {
	jobs      chan trackJob
	downloads chan struct{} // semaphore: concurrent downloads
	converts  chan struct{} // semaphore: concurrent ffmpeg processes
	notify    chan struct{} // a job finished

	mu      sync.Mutex
	done    map[int]trackResult
	pending int // queued and not yet applied

	nextSeq  int             // next job to queue; main goroutine only
	applySeq int             // next job to apply; main goroutine only
	inflight map[string]bool // finalPath of queued jobs; main goroutine only
}

// parallelDownloads is how many tracks download at the same time.
func (r *Runner) parallelDownloads() int {
	if n := r.Config.General.ParallelDownloads; n > 1 {
		return n
	}
	return 1
}

// queueJob hands a job to the background workers, starting them on first use.
func (r *Runner) queueJob(job trackJob) {
	if r.bg == nil {
		n := r.parallelDownloads()
		workers := postWorkers
		if n > 1 {
			// Enough workers for n downloads plus the tracks being processed.
			workers = n + r.parallelConverts()
		}
		w := &bgWorker{
			jobs:      make(chan trackJob, 4096),
			downloads: make(chan struct{}, n),
			converts:  make(chan struct{}, r.parallelConverts()),
			notify:    make(chan struct{}, 1),
			done:      make(map[int]trackResult),
			inflight:  make(map[string]bool),
		}
		r.bg = w
		for i := 0; i < workers; i++ {
			go func() {
				for job := range w.jobs {
					res := r.runJob(job)
					w.mu.Lock()
					w.done[job.seq] = res
					w.mu.Unlock()
					select {
					case w.notify <- struct{}{}:
					default:
					}
				}
			}()
		}
	}
	r.startTimingsClock()
	job.seq = r.bg.nextSeq
	r.bg.nextSeq++
	r.bg.inflight[job.finalPath] = true
	r.bg.mu.Lock()
	r.bg.pending++
	r.bg.mu.Unlock()
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
	setState := func(st liveState, step string) {
		job.view.update(job.item, func(it *liveItem) { it.state, it.step = st, step })
	}
	fail := func(counter string, format string, args ...any) trackResult {
		fmt.Fprintf(&out, format+"\n", args...)
		reason := fmt.Sprintf(format, args...)
		job.view.update(job.item, func(it *liveItem) { it.state, it.step = liveFailed, reason })
		// Drop the unfinished file, so a retry downloads the track again.
		if job.workDir != "" {
			os.RemoveAll(job.workDir)
		} else if job.post {
			os.Remove(job.partPath)
		}
		if job.download && job.coverToRemove != "" {
			os.Remove(job.coverToRemove)
		}
		res.counter = counter
		res.output = out.String()
		return res
	}

	if job.download {
		var lyricsCh chan lyricsResult
		if job.lyrics {
			lyricsCh = make(chan lyricsResult, 1)
			go func() {
				lrc, err := lyrics.Get(job.track.ID, r.Config.Metadata.Lyrics.Type, r.Config.General.Language, r.Config.Metadata.Lyrics.Format, r.Config.General.LiteServer, r.Config.Metadata.Lyrics.Extra)
				lyricsCh <- lyricsResult{lrc: lrc, err: err}
			}()
		}

		r.bg.downloads <- struct{}{}
		last = time.Now() // don't count the wait for a download slot
		job.view.update(job.item, func(it *liveItem) { it.state, it.started = liveDownloading, last })
		req := downloadRequest{AdamID: job.track.ID, Out: job.partPath}
		var err error
		if job.aacLC {
			req.Mode = "aac-lc"
			job.view.update(job.item, func(it *liveItem) { it.quality = "AAC 256 kbps" })
		} else {
			req.Mode = "fairplay"
			var quality string
			req.URL, quality, err = r.extractMediaTo(&out, job.track.M3u8, false)
			job.view.update(job.item, func(it *liveItem) { it.quality = formatQuality(quality, r.Flags.Atmos, r.Flags.AAC) })
			if err != nil {
				<-r.bg.downloads
				if lyricsCh != nil {
					<-lyricsCh
				}
				return fail("unavailable", "⚠ Failed to extract info from manifest: %v", err)
			}
		}
		if err == nil {
			var progress func(done, total int64)
			if job.item != nil {
				progress = func(done, total int64) {
					job.view.update(job.item, func(it *liveItem) { it.done, it.total = done, total })
				}
			}
			err = downloadTrack(r, req, progress)
		}
		<-r.bg.downloads
		if err != nil {
			if lyricsCh != nil {
				<-lyricsCh
			}
			if errors.Is(err, errUnavailable) {
				return fail("unavailable", "Failed to download: Unavailable")
			}
			return fail("error", "Failed to download: %v", err)
		}
		mark("download")
		if job.workDir != "" {
			// Large tracks are written to a new file and renamed over the
			// pre-created one, which loses the temporary attribute (Windows).
			_ = fsutil.MarkTemporary(job.partPath)
		}
		if fi, err := os.Stat(job.partPath); err == nil {
			res.bytes = fi.Size()
			d := res.stages[len(res.stages)-1].d
			fmt.Fprintf(&out, "Downloaded %s\n", speed(res.bytes, d))
			job.view.update(job.item, func(it *liveItem) { it.downloadB, it.finished = res.bytes, d })
		}

		if lyricsCh != nil {
			lr := <-lyricsCh
			switch {
			case errors.Is(lr.err, lyrics.ErrLyricsNotFound):
				fmt.Fprintln(&out, "No lyrics available for this song")
				job.view.update(job.item, func(it *liveItem) { it.note = "no lyrics" })
			case lr.err != nil:
				fmt.Fprintln(&out, lr.err)
			case lr.lrc != "":
				if r.Config.Metadata.Lyrics.SaveFile {
					if err := r.writeLyrics(job.track.SaveDir, job.lrcFilename, lr.lrc); err != nil {
						fmt.Fprintln(&out, "Failed to write lyrics")
					}
				}
				if r.Config.Metadata.Lyrics.Embed {
					job.lrc = lr.lrc
				}
			}
			mark("lyrics")
		}
	}

	if job.post {
		setState(liveProcessing, "processing")
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

		if job.cover != nil {
			path, err := job.cover.wait()
			if err != nil {
				fmt.Fprintf(&out, "Failed to write cover: %v\n", err)
			}
			job.track.CoverPath = path
		}
		if err := r.writeMP4Tags(job.track, job.partPath, job.lrc); err != nil {
			return fail("unavailable", "⚠ Failed to write tags in media: %v", err)
		}
		tagged := job.finalPath
		if job.workDir != "" {
			tagged = filepath.Join(job.workDir, filepath.Base(job.finalPath))
		}
		if err := os.Rename(job.partPath, tagged); err != nil {
			return fail("error", "Failed to rename %s: %v", filepath.Base(job.partPath), err)
		}
		mark("tags")

		if job.coverToRemove != "" {
			if err := os.Remove(job.coverToRemove); err != nil {
				fmt.Fprintf(&out, "Error deleting file: %s\n", job.coverToRemove)
				job.view.update(job.item, func(it *liveItem) { it.state, it.step = liveFailed, "could not delete the track's cover" })
				res.counter = "error"
				res.output = out.String()
				return res
			}
		}
	}

	if job.convert {
		setState(liveConverting, "waiting to convert")
		r.bg.converts <- struct{}{}
		last = time.Now() // don't count the wait for an ffmpeg slot
		setState(liveConverting, "converting to "+strings.ToUpper(job.convertJob.targetFmt))
		if outPath, ok := r.runConvert(job.convertJob, &out); ok {
			res.path = outPath
		}
		<-r.bg.converts
		mark("convert")
	}

	if job.workDir != "" {
		// Move the finished file(s) to the save folder.
		setState(liveProcessing, "moving to the save folder")
		destDir := filepath.Dir(job.finalPath)
		moved, err := deliverWork(job.workDir, destDir)
		if err != nil {
			return fail("error", "\u26A0 %v", err)
		}
		if p, ok := moved[filepath.Base(res.path)]; ok {
			res.path = p
		}
		mark("move")
	}
	setState(liveDone, "")
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
		r.bg.pending--
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
	if job.view != nil {
		// The live list shows the outcome; keep a failed track's messages to
		// print after the list.
		if res.counter != "" {
			job.view.update(job.item, func(it *liveItem) {
				it.notes = append(it.notes, strings.Split(strings.TrimSpace(res.output), "\n")...)
			})
		}
	} else {
		fmt.Printf("[background] %s\n", filepath.Base(job.finalPath))
		fmt.Print(res.output)
	}
	for _, s := range res.stages {
		r.addBackgroundTiming(s.name, s.d)
	}
	r.addBackgroundBytes(res.bytes)
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

// waitJobs blocks until every queued job is done, applying (and printing)
// each one as soon as it and all jobs before it have finished.
func (r *Runner) waitJobs() {
	r.waitCovers() // also covers no queued track waits for (all tracks existed)
	if r.bg == nil {
		return
	}
	start := time.Now()
	for {
		r.collectJobs()
		r.bg.mu.Lock()
		left := r.bg.pending
		r.bg.mu.Unlock()
		if left == 0 {
			break
		}
		<-r.bg.notify
	}
	r.addWaitTiming(time.Since(start))
}

// pendingCover is an album cover that is still downloading. An album's tracks
// are queued without waiting for it (with artwork "original" Apple renders a
// large PNG, which can take seconds) and wait for it only when they are
// tagged, so downloads keep going meanwhile.
type pendingCover struct {
	done chan struct{}
	path string
	err  error
}

func (p *pendingCover) wait() (string, error) {
	<-p.done
	return p.path, p.err
}

// waitCovers waits for the album covers still downloading.
func (r *Runner) waitCovers() {
	if r.coverWG != nil {
		r.coverWG.Wait()
	}
}

// startCover downloads an album cover in the background; tracks queued after
// it take it as their cover.
func (r *Runner) startCover(dir, url string) {
	p := &pendingCover{done: make(chan struct{})}
	if r.coverWG == nil {
		r.coverWG = &sync.WaitGroup{}
	}
	r.coverWG.Add(1)
	go func() {
		defer r.coverWG.Done()
		defer close(p.done)
		p.path, p.err = r.writeCover(dir, "cover", url)
	}()
	r.albumCover = p
}
