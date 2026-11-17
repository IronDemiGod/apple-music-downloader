package app

import (
	"amdl/internal/config"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	forbiddenNames = regexp.MustCompile(`[/\\<>:"|?*]`)
)

type Flags struct {
	Atmos          bool
	AAC            bool
	Select         bool
	ArtistSelect   bool
	Debug          bool
	PrintJSON      bool
	SaveM3U8       bool
	LiteServerFlag string
	Version        bool
	Update         bool
	CheckUpdate    bool
	Yes            bool
	ForceUpdate    bool
	MigrateConfig  bool
	Timings        bool
	NoLive         bool
}

type State struct {
	Counter     Counter
	OKDict      map[string][]int
	AddedTracks []AddedTrack
	Timings     Timings
}

type Runner struct {
	Config config.ConfigSet
	Flags  Flags
	State  State
	bg     *bgWorker // background post-processing workers, started on first use
	live   *liveView // live progress list of the album/playlist being downloaded
	// liveHeld keeps the live list open across albums (a playlist sorted
	// into album folders shows one list): endLive does nothing while set.
	liveHeld bool

	// covers caches cover downloads while albums are prefetched (prefetch.go);
	// prefetching marks the prefetch workers' own runner.
	covers      *coverCache
	prefetching bool

	// sayItem is the live list entry of the track being prepared on the main
	// goroutine; sayf/sayln send its messages there instead of the screen.
	sayItem *liveItem

	// This run's folder under paths.work-dir ("" until first used, or unused).
	workRun       string
	workRunFailed bool
}

// sayf prints a message about the current track, or, while the live list is
// shown, keeps it in the track's entry (printed after the list if it failed).
// Main goroutine only.
func (r *Runner) sayf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if r.live == nil {
		fmt.Print(msg)
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" || r.sayItem == nil {
		return
	}
	r.live.update(r.sayItem, func(it *liveItem) { it.notes = append(it.notes, msg) })
}

// sayln is sayf with fmt.Println formatting.
func (r *Runner) sayln(a ...any) {
	r.sayf("%s", fmt.Sprintln(a...))
}

func NewRunner(cfg config.ConfigSet) *Runner {
	return &Runner{Config: cfg, State: State{OKDict: make(map[string][]int)}}
}

type AddedTrack struct {
	Path     string `json:"path"`
	Artist   string `json:"artist"`
	ArtistID string `json:"artist_id"`
	Album    string `json:"album"`
	Song     string `json:"song"`
}

// topLevelKeys returns the set of top-level YAML keys in data.

// contains reports whether item is present in slice.
func contains[T comparable](slice []T, item T) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func (r *Runner) LimitString(s string) string {
	if len([]rune(s)) > r.Config.Paths.LimitMax {
		return string([]rune(s)[:r.Config.Paths.LimitMax])
	}
	return s
}

func fileExists(path string) (bool, error) {
	f, err := os.Stat(path)
	if err == nil {
		return !f.IsDir(), nil
	} else if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
