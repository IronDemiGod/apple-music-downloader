// coverfix re-downloads the album covers of an amdl music library in their
// original format and removes the old cover.* images.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/pflag"

	ampapi "amdl/internal/amp-api"
	"amdl/internal/app"
	"amdl/internal/config"
	"amdl/internal/download"
)

const helpText = `coverfix - redo the album covers of an amdl music library

USAGE
  coverfix --cover FORMAT --embed FORMAT [OPTIONS] [FOLDER ...]
  (every option takes its value after a space or an =: --embed png, --embed=png)

REQUIRED
  --cover FORMAT     the cover file saved in each album folder (cover.jpg / cover.png)
  --embed FORMAT     the picture embedded in every track (.flac and .m4a)

FORMAT
  jpg or png, optionally followed by a resolution from 100 to 10000:
    png              full-size PNG: the source pixels, lossless (~10-15 MB)
    png5000          PNG of at most 5000x5000
    jpg              full-size JPEG, re-encoded by Apple (~1-2 MB)
    jpg3000          JPEG of at most 3000x3000
  Apple never upscales: every size is capped at the album's source resolution.

OPTIONS
  -n, --dry-run      show what would be changed; change nothing, look nothing up
  -j, --parallel N   albums processed at once (default 4)
      --in FOLDER    the FOLDER arguments are names inside FOLDER; several may be
                     given in one argument, separated by commas:
                       coverfix --cover png --embed png --in "C:/Music" "ADO,Pink Floyd,King Gnu"
                     A name that contains a comma itself is used whole when that
                     folder exists. Without FOLDER arguments, all of FOLDER is done.
      --no-live      plain output instead of the live progress bars
  -c, --config FILE  amdl config to read proxy, storefront, language and music
                     folders from (default: config.yaml)
      --storefront CC  storefront to look albums up in (default: the config's
                     general.storefront)
  -h, --help         show this help

FOLDERS
  Every folder given is scanned together with all its subfolders, so a folder
  can be one album, one artist, or the whole library. With no folder, the alac,
  atmos and aac folders from the config are used.

WHAT IT DOES, FOR EACH ALBUM FOLDER
  1. Finds the album from the tracks' tags: the iTunes album ID (.m4a), else the
     UPC, else a track's ISRC matched against the album name.
  2. Downloads the cover in the --cover format and saves it as cover.<ext>.
  3. Deletes the folder's other cover.* images, only after step 2 succeeded.
  4. Replaces the embedded picture of every track with one in the --embed
     format. Only the picture changes: tags and audio stay exactly as they were.
  It skips folders with tracks from several albums, playlist folders (album
  artist "Apple Music"), and folders without .flac or .m4a files.

NOTES
  - FLAC can embed a picture of at most about 16 MB. A full-size PNG usually
    fits, but if a track reports "too large", use e.g. --embed png4000 or jpg.
  - A full-size PNG embedded in every track adds 10-15 MB per track.
  - Exit code: 0 when everything worked, 1 when an album or track failed,
    2 for wrong arguments.

EXAMPLES
  coverfix --cover png --embed jpg3000 -n "C:\Music"     preview the whole library
  coverfix --cover png --embed jpg3000 "C:\Music"        do it
  coverfix --cover png --embed png "C:\Music\Artist"     one artist, full-size PNG everywhere
  coverfix --cover jpg --embed jpg1400                   folders from config.yaml
`

func main() {
	var configFile, storefront, coverArg, embedArg, inDir string
	var dryRun, noLive bool
	var parallel int
	flags := pflag.NewFlagSet("coverfix", pflag.ContinueOnError)
	flags.StringVar(&coverArg, "cover", "", "format of the folder's cover file")
	flags.StringVar(&embedArg, "embed", "", "format of the picture embedded in every track")
	flags.StringVarP(&configFile, "config", "c", "config.yaml", "amdl config file")
	flags.StringVar(&storefront, "storefront", "", "storefront to look albums up in")
	flags.BoolVarP(&dryRun, "dry-run", "n", false, "only show what would be changed")
	flags.IntVarP(&parallel, "parallel", "j", 4, "albums processed at once")
	flags.StringVar(&inDir, "in", "", "parent folder of the FOLDER arguments (comma-separated names allowed)")
	flags.BoolVar(&noLive, "no-live", false, "plain output instead of the live progress bars")
	flags.Usage = func() { fmt.Fprint(os.Stdout, helpText) }
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, "Run 'coverfix --help' for usage.")
		os.Exit(2)
	}

	if coverArg == "" || embedArg == "" {
		fmt.Fprintln(os.Stderr, "error: --cover and --embed are both required, e.g. --cover png --embed jpg3000")
		fmt.Fprintln(os.Stderr, "Run 'coverfix --help' for usage.")
		os.Exit(2)
	}
	coverSpec, err := app.ParseArtSpec(coverArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: --cover", err)
		os.Exit(2)
	}
	embedSpec, err := app.ParseArtSpec(embedArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: --embed", err)
		os.Exit(2)
	}

	cfg := config.Default()
	if _, err := os.Stat(configFile); err == nil {
		loaded, err := config.Load(config.LoadOptions{ConfigFile: configFile, DisableMissingWarnings: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, "load config:", err)
			os.Exit(1)
		}
		cfg = *loaded
	}
	if storefront == "" {
		storefront = cfg.General.Storefront
	}

	roots, err := cleanFolderArgs(flags.Args(), runtime.GOOS == "windows")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if inDir != "" {
		cleaned, err := cleanFolderArgs([]string{inDir}, runtime.GOOS == "windows")
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: --in", err)
			os.Exit(2)
		}
		var missing []string
		roots, missing = resolveIn(cleaned[0], roots)
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "warning: no folder %q in %s, skipped\n", m, cleaned[0])
		}
		if len(roots) == 0 {
			fmt.Fprintln(os.Stderr, "error: none of the given folders exist in", cleaned[0])
			os.Exit(1)
		}
	}
	if len(roots) == 0 {
		for _, p := range []string{cfg.Paths.Alac, cfg.Paths.Atmos, cfg.Paths.Aac} {
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				roots = append(roots, p)
			}
		}
	}
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "no music folder found; pass one as an argument")
		os.Exit(1)
	}

	if err := download.Init(cfg.General.Proxy); err != nil {
		fmt.Fprintln(os.Stderr, "proxy config:", err)
		os.Exit(1)
	}
	token, err := ampapi.GetToken()
	if err != nil {
		if t := cfg.General.AuthorizationToken; t != "" && t != "your-authorization-token" {
			token = strings.TrimPrefix(t, "Bearer ")
		} else {
			fmt.Fprintln(os.Stderr, "failed to get Apple Music token:", err)
			os.Exit(1)
		}
	}

	fmt.Printf("cover: %s  |  embed: %s\n", coverSpec, embedSpec)
	opt := app.CoverFixOptions{Cover: coverSpec, Embed: embedSpec, DryRun: dryRun, Parallel: parallel}
	r := app.NewRunner(cfg)
	r.Flags.NoLive = noLive
	var total app.CoverFixResult
	for _, root := range roots {
		fmt.Println("==>", root)
	}
	total, err = r.RefreshCovers(roots, storefront, token, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	verb := "Albums done"
	if dryRun {
		verb = "Albums to do"
	}
	fmt.Printf("\n%s: %d  |  Skipped: %d  |  Failed: %d\n", verb, total.Refreshed, total.Skipped, total.Failed)
	if !dryRun {
		fmt.Printf("Tracks embedded: %d  |  Track errors: %d\n", total.TracksEmbedded, total.TracksFailed)
	}
	if total.Failed > 0 || total.TracksFailed > 0 {
		os.Exit(1)
	}
}

// cleanFolderArgs undoes a Windows quoting trap: in "C:\Music\Album\" the
// final \" is an escaped quote, so the folder arrives as C:\Music\Album".
// Windows names cannot contain '"', so on Windows a trailing quote is dropped
// and any other quote is reported instead of being looked up as a folder.
// resolveIn turns names given with --in into folders inside base. An argument
// that is a folder inside base (or an absolute path) is used as is; otherwise
// it is split on commas, so "ADO,Pink Floyd, King Gnu" names three folders.
// Without names, base itself is returned. missing lists names not found.
func resolveIn(base string, names []string) (dirs, missing []string) {
	if len(names) == 0 {
		return []string{base}, nil
	}
	isDir := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, name)
		}
		if isDir(p) {
			dirs = append(dirs, p)
		} else {
			missing = append(missing, name)
		}
	}
	for _, a := range names {
		whole := a
		if !filepath.IsAbs(whole) {
			whole = filepath.Join(base, a)
		}
		if isDir(whole) || !strings.Contains(a, ",") {
			add(a)
			continue
		}
		for _, part := range strings.Split(a, ",") {
			add(part)
		}
	}
	return dirs, missing
}

func cleanFolderArgs(args []string, windows bool) ([]string, error) {
	if !windows {
		return args, nil
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimRight(strings.TrimSpace(a), `"`)
		if strings.Contains(a, `"`) {
			return nil, fmt.Errorf("folder argument %q contains a quote; remove the backslash before the closing quote, e.g. \"C:\\Music\\Album\" instead of \"C:\\Music\\Album\\\"", a)
		}
		out = append(out, a)
	}
	return out, nil
}
