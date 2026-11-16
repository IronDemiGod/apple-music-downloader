package app

import (
	"bytes"
	"fmt"
	"io"
	"amdl/internal/model"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (r *Runner) isLossySource(ext string, codec string) bool {
	ext = strings.ToLower(ext)
	if ext == ".m4a" && (codec == "AAC" || strings.Contains(codec, "AAC") || strings.Contains(codec, "ATMOS")) {
		return true
	}
	if ext == ".mp3" || ext == ".opus" || ext == ".ogg" {
		return true
	}
	return false
}

// CONVERSION FEATURE: Build ffmpeg arguments for desired target.

func (r *Runner) buildFFmpegArgs(ffmpegPath, inPath, outPath, targetFmt, extraArgs string) ([]string, error) {
	args := []string{"-y", "-i", inPath, "-loglevel", "error", "-map_metadata"}
	if r.Config.Convert.WithMetadata {
		args = append(args, "0")
	} else {
		args = append(args, "-1")
	}
	switch targetFmt {
	case "flac":
		args = append(args, "-c:a", "flac")
		args = append(args, coverArtArgs()...)
	case "mp3":
		// VBR quality 2 ~ high quality
		args = append(args, "-c:a", "libmp3lame", "-qscale:a", "2")
		args = append(args, coverArtArgs()...)
		args = append(args, mp3TagArgs()...)
	case "opus":
		// Ogg Opus cannot carry an attached picture, so drop any video stream
		// explicitly rather than relying on ffmpeg's stream auto-selection.
		args = append(args, "-vn", "-c:a", "libopus", "-b:a", "192k", "-vbr", "on")
	case "wav":
		// WAV has no attached-picture support either; audio only.
		args = append(args, "-vn", "-c:a", "pcm_s16le")
	case "copy":
		// Just container copy (probably pointless for same container)
		args = append(args, "-c", "copy")
	default:
		return nil, fmt.Errorf("unsupported convert-format: %s", targetFmt)
	}
	if extraArgs != "" {
		// naive split; for complex quoting you could enhance
		args = append(args, strings.Fields(extraArgs)...)
	}
	args = append(args, outPath)
	return args, nil
}

// coverArtArgs maps every stream and copies the embedded cover (attached_pic)
// over verbatim, so album art survives the conversion instead of being dropped
// (no -map) or re-encoded (ffmpeg turns a jpg cover into a png on its own).
//
// Only formats whose muxer supports attached pictures may use this: the ogg
// based muxers (opus) reject a picture stream with "Unsupported codec id in
// stream N" and leave an empty file behind.
func coverArtArgs() []string {
	return []string{"-map", "0", "-c:v", "copy", "-disposition:v", "attached_pic"}
}

// mp3TagArgs makes the cover readable for players that are picky about ID3:
// ffmpeg writes ID3v2.4 by default, which Windows Explorer will not read a
// cover from, and leaves the APIC picture type at "Other". The stream comment
// maps to the picture type and the title to the APIC description.
func mp3TagArgs() []string {
	return []string{
		"-id3v2_version", "3",
		"-metadata:s:v", "title=Album cover",
		"-metadata:s:v", "comment=Cover (front)",
	}
}

// CONVERSION FEATURE: Perform conversion if enabled.

// convertJob is one prepared ffmpeg conversion. It holds copies of everything
// it needs, so it can run on a background goroutine without touching the track.
type convertJob struct {
	srcPath   string
	outPath   string
	targetFmt string
	args      []string
	// addedIdx is the track's index in State.AddedTracks, whose Path is
	// updated once the conversion is done; -1 when there is none.
	addedIdx int
}

// convertIfNeeded converts the track right away (in the foreground).
func (r *Runner) convertIfNeeded(track *model.Track) {
	job, ok := r.prepareConvert(track.SavePath, track.Codec)
	if !ok {
		return
	}
	if outPath, ok := r.runConvert(job, os.Stdout); ok {
		track.SavePath = outPath
		track.SaveName = filepath.Base(outPath)
	}
}

// prepareConvert runs the quick checks (enabled, format, lossy source, ffmpeg
// present) and builds the ffmpeg command. It prints its messages directly and
// must be called on the main goroutine.
func (r *Runner) prepareConvert(srcPath, codec string) (convertJob, bool) {
	if !r.Config.Convert.AfterDownload {
		return convertJob{}, false
	}
	if r.Config.Convert.Format == "" {
		return convertJob{}, false
	}
	if srcPath == "" {
		return convertJob{}, false
	}
	ext := strings.ToLower(filepath.Ext(srcPath))
	targetFmt := strings.ToLower(r.Config.Convert.Format)

	// Map extension for output
	if targetFmt == "copy" {
		fmt.Println("Convert (copy) requested; skipping because it produces no new format.")
		return convertJob{}, false
	}

	if r.Config.Convert.SkipIfSourceMatch {
		if ext == "."+targetFmt {
			fmt.Printf("Conversion skipped (already %s)\n", targetFmt)
			return convertJob{}, false
		}
	}

	outBase := strings.TrimSuffix(srcPath, ext)
	outPath := outBase + "." + targetFmt

	// Handle lossy -> lossless cases: optionally skip or warn
	if (targetFmt == "flac" || targetFmt == "wav") && r.isLossySource(ext, codec) {
		if r.Config.Convert.SkipLossyToLossless {
			fmt.Println("Skipping conversion: source appears lossy and target is lossless; configured to skip.")
			return convertJob{}, false
		}
		if r.Config.Convert.WarnLossyToLossless {
			fmt.Println("Warning: Converting lossy source to lossless container will not improve quality.")
		}
	}

	if _, err := exec.LookPath(r.Config.Convert.FFmpegPath); err != nil {
		fmt.Printf("ffmpeg not found at '%s'; skipping conversion.\n", r.Config.Convert.FFmpegPath)
		return convertJob{}, false
	}

	args, err := r.buildFFmpegArgs(r.Config.Convert.FFmpegPath, srcPath, outPath, targetFmt, r.Config.Convert.ExtraArgs)
	if err != nil {
		fmt.Println("Conversion config error:", err)
		return convertJob{}, false
	}
	return convertJob{srcPath: srcPath, outPath: outPath, targetFmt: targetFmt, args: args, addedIdx: -1}, true
}

// runConvert runs ffmpeg for a prepared job and writes its messages to w.
// It only reads r.Config, so it is safe to call from a background goroutine.
// It returns the path of the converted file and whether conversion succeeded.
func (r *Runner) runConvert(job convertJob, w io.Writer) (string, bool) {
	srcPath, outPath, targetFmt := job.srcPath, job.outPath, job.targetFmt
	fmt.Fprintf(w, "Converting -> %s ...\n", targetFmt)
	cmd := exec.Command(r.Config.Convert.FFmpegPath, job.args...)
	var stderr bytes.Buffer
	if r.Config.Convert.CheckBadALAC {
		cmd.Stderr = &stderr
	} else {
		cmd.Stderr = nil
	}
	cmd.Stdout = nil
	start := time.Now()
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(w, "Conversion failed:", err)
		// leave original
		return "", false
	}
	if r.Config.Convert.CheckBadALAC && stderr.Len() > 0 {
		fmt.Fprint(w, "Detected ALAC Error.", "\n")
		if r.Config.Convert.DeleteBadALAC {
			delPath := strings.TrimSuffix(srcPath, "m4a") + targetFmt
			logPath := strings.TrimSuffix(srcPath, "m4a") + "log"
			if err := os.Remove(delPath); err != nil {
				fmt.Fprintln(w, "Failed to remove convert:", err)
			} else {
				fmt.Fprintln(w, "Convert removed due to the bad ALAC.")
				log := stderr
				err = os.WriteFile(logPath, log.Bytes(), 0644)
				if err != nil {
					fmt.Fprintln(w, "Convert logs:", log.String())
				} else {
					fmt.Fprintln(w, "Convert logs are stored in:", logPath)
				}
			}
		}
		return "", false
	}
	fmt.Fprintf(w, "Conversion completed in %s: %s\n", time.Since(start).Truncate(time.Millisecond), filepath.Base(outPath))

	if !r.Config.Convert.KeepOriginal {
		if err := os.Remove(srcPath); err != nil {
			fmt.Fprintln(w, "Failed to remove original after conversion:", err)
		} else {
			fmt.Fprintln(w, "Original removed.")
		}
	}
	return outPath, true
}
