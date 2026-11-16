package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"amdl/internal/config"
	"amdl/internal/download"
	fairplayrip "amdl/internal/fairplay-rip"
	"amdl/internal/widevine-rip/runv5"
)

// Parallel downloads run each download in a child amdl process started with
// workerArg. The child runs the same, unchanged download code as a normal
// download; its standard output (the "Decrypting..." progress bar, which
// would be garbled with several downloads at once) is discarded, and amdl
// prints its own line per track instead. The request, including the config
// with its tokens, goes to the child over stdin, never on the command line.

const workerArg = "--download-worker"

// Exit codes of the download worker.
const (
	workerOK          = 0
	workerFailed      = 1
	workerUnavailable = 3
)

type downloadRequest struct {
	Config config.ConfigSet
	Mode   string // "fairplay" or "aac-lc"
	AdamID string
	URL    string // variant playlist (fairplay)
	Out    string
}

// downloadTrack downloads one track for a background job; tests replace it.
// progress, when not nil, is called with the bytes done and the total.
var downloadTrack = (*Runner).downloadInWorker

// extraWorkerModes lets tests add download modes to the worker; empty in amdl.
var extraWorkerModes = map[string]func(downloadRequest) error{}

// errUnavailable marks a track Apple does not offer (counted as a warning).
var errUnavailable = errors.New("Unavailable")

// IsDownloadWorker reports whether this process was started as a worker.
func IsDownloadWorker() bool {
	return len(os.Args) == 2 && os.Args[1] == workerArg
}

// RunDownloadWorker runs one download request from stdin and returns the
// process exit code. Errors go to stderr, which the parent reports.
func RunDownloadWorker() int {
	var req downloadRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "download worker: bad request:", err)
		return workerFailed
	}
	if err := download.Init(req.Config.General.Proxy); err != nil {
		fmt.Fprintln(os.Stderr, "proxy config error:", err)
		return workerFailed
	}
	var err error
	switch req.Mode {
	case "aac-lc":
		err = runv5.DownloadSong(context.Background(), req.AdamID, req.Out, req.Config.General.LiteServer)
	case "fairplay":
		err = fairplayrip.Run(req.AdamID, req.URL, req.Out, req.Config)
	default:
		if f, ok := extraWorkerModes[req.Mode]; ok {
			err = f(req)
		} else {
			err = fmt.Errorf("download worker: unknown mode %q", req.Mode)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if err.Error() == "Unavailable" {
			return workerUnavailable
		}
		return workerFailed
	}
	return workerOK
}

// downloadInWorker downloads one track in a child process. It only reads
// r.Config, so it is safe to call from a background goroutine. The child's
// stdout (the download's progress bar) is discarded, or parsed for progress
// when progress is not nil.
func (r *Runner) downloadInWorker(req downloadRequest, progress func(done, total int64)) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find amdl executable: %w", err)
	}
	req.Config = r.Config
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, workerArg)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if progress == nil {
		cmd.Stdout = nil // discarded: the progress bar
		err = cmd.Run()
	} else {
		var stdout io.ReadCloser
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err = cmd.Start(); err != nil {
			return err
		}
		parseProgress(stdout, progress) // returns when the child closes stdout
		err = cmd.Wait()
	}
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(stderr.String())
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == workerUnavailable {
		return errUnavailable
	}
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(msg)
}
