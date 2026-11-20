package app

import (
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Cleanup on Ctrl+C (or SIGTERM): without it, an interrupted run leaves the
// terminal's cursor hidden (the live views hide it while they redraw) and its
// work folder (RAM on Linux, .amdl-work on Windows) until the next start.
// The download workers are in the same process group and get Ctrl+C
// themselves; the main process removes what it owns and exits.

var interruptState struct {
	once    sync.Once
	mu      sync.Mutex
	cursor  io.Writer // where the cursor was hidden, nil when it is shown
	workRun string    // this run's work folder, removed on interrupt
}

func watchInterrupt() {
	interruptState.once.Do(func() {
		// Started with Ctrl+C ignored (e.g. in the background): keep it so.
		if signal.Ignored(os.Interrupt) {
			return
		}
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			s := &interruptState
			s.mu.Lock()
			if s.cursor != nil {
				io.WriteString(s.cursor, "\x1b[?2026l\x1b[?25h\n")
			}
			if s.workRun != "" {
				os.RemoveAll(s.workRun)
			}
			os.Exit(130)
		}()
	})
}

// cursorHidden records that the cursor was hidden on w (nil: shown again).
func cursorHidden(w io.Writer) {
	watchInterrupt()
	interruptState.mu.Lock()
	interruptState.cursor = w
	interruptState.mu.Unlock()
}

// removeOnInterrupt records this run's work folder.
func removeOnInterrupt(dir string) {
	watchInterrupt()
	interruptState.mu.Lock()
	interruptState.workRun = dir
	interruptState.mu.Unlock()
}
