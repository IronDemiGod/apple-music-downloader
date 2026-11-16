package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Work folder (paths.work-dir): when set, a track is downloaded and processed
// there (defrag, alac-fix, tags, conversion) and only the finished file is
// moved to its save folder. That matters when the save folder is slow, e.g. a
// USB drive behind a FUSE NTFS driver, where every rewrite of a large file is
// costly; a RAM disk (/dev/shm on Linux) makes all of it nearly free.
//
// Layout: <work-dir>/run-<pid>/<track id>/<file name>. Each run has its own
// folder, removed at the end; folders of runs that are no longer alive (killed
// runs) are removed when the next run first uses the work folder.

// workSpacePerTrack is the free space a track may need in the work folder
// (a hi-res .m4a and its FLAC are ~300 MB together; with margin).
const workSpacePerTrack = 512 << 20

// workRunDir returns this run's work folder, creating it (and cleaning up
// after dead runs) on first use. "" when no work-dir is configured or it
// cannot be used. Main goroutine only.
func (r *Runner) workRunDir() string {
	if r.workRun != "" || r.workRunFailed {
		return r.workRun
	}
	root := r.Config.Paths.WorkDir
	if strings.TrimSpace(root) == "auto" {
		root = autoWorkDir()
	}
	root = expandPath(root)
	if root == "" {
		r.workRunFailed = true
		return ""
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		fmt.Printf("⚠ work-dir %s cannot be used (%v); working in the save folders.\n", root, err)
		r.workRunFailed = true
		return ""
	}
	removeDeadRuns(root)
	run := filepath.Join(root, "run-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(run, 0o700); err != nil {
		fmt.Printf("⚠ work-dir %s cannot be used (%v); working in the save folders.\n", root, err)
		r.workRunFailed = true
		return ""
	}
	r.workRun = run
	return run
}

// trackWorkDir returns a fresh work folder for one track, or "" to work in
// the save folder (no work-dir, or not enough free space in it).
func (r *Runner) trackWorkDir(trackID string) string {
	run := r.workRunDir()
	if run == "" {
		return ""
	}
	need := int64(workSpacePerTrack) * int64(r.parallelDownloads()+maxConverts+1)
	if free, err := freeBytes(run); err == nil && free < need {
		r.sayf("work-dir has only %d MB free; this track works in its save folder.\n", free>>20)
		return ""
	}
	dir := filepath.Join(run, sanitizeWorkName(trackID))
	os.RemoveAll(dir) // a leftover from the same song earlier in this run
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.sayf("work-dir: %v; this track works in its save folder.\n", err)
		return ""
	}
	return dir
}

// autoWorkDir is the work folder for work-dir "auto": RAM on Linux
// (/dev/shm), none elsewhere (no RAM disk is available by default).
func autoWorkDir() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		return "/dev/shm/amdl"
	}
	return ""
}

// removeWorkRun deletes this run's work folder (at the end of the run).
func (r *Runner) removeWorkRun() {
	if r.workRun != "" {
		os.RemoveAll(r.workRun)
		r.workRun = ""
	}
}

// removeDeadRuns deletes the run folders of amdl processes that have ended.
func removeDeadRuns(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, "run-") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(name, "run-"))
		if err != nil || pid == os.Getpid() || pidAlive(pid) {
			continue
		}
		os.RemoveAll(filepath.Join(root, name))
	}
}

func sanitizeWorkName(s string) string {
	s = forbiddenNames.ReplaceAllString(s, "_")
	if s == "" || s == "." || s == ".." {
		return "track"
	}
	return s
}

// expandPath expands a leading "~" and environment variables ($VAR, and
// %VAR% on Windows).
func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	p = os.Expand(p, os.Getenv)
	for {
		i := strings.Index(p, "%")
		if i < 0 {
			break
		}
		j := strings.Index(p[i+1:], "%")
		if j <= 0 {
			break
		}
		name := p[i+1 : i+1+j]
		v, ok := os.LookupEnv(name)
		if !ok {
			break
		}
		p = p[:i] + v + p[i+2+j:]
	}
	return filepath.Clean(p)
}

// moveFile moves src to dst. Within one filesystem it is a rename; across
// filesystems (e.g. RAM to a USB drive) the file is copied to a temporary
// ".part" name next to dst and renamed when complete, so dst never exists half
// written, and then src is removed.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	ext := filepath.Ext(dst)
	tmp := strings.TrimSuffix(dst, ext) + ".part" + ext
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	in.Close()
	return os.Remove(src)
}

// deliverWork moves every finished file of a track's work folder (anything
// but temporary ".part" files) to destDir and removes the work folder. It
// returns where each file went, by name.
func deliverWork(workDir, destDir string) (map[string]string, error) {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil, err
	}
	moved := make(map[string]string)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.Contains(name, ".part.") {
			continue
		}
		dst := filepath.Join(destDir, name)
		if err := moveFile(filepath.Join(workDir, name), dst); err != nil {
			return moved, fmt.Errorf("move %s to %s: %w", name, destDir, err)
		}
		moved[name] = dst
	}
	return moved, os.RemoveAll(workDir)
}
