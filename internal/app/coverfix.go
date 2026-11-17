package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/itouakirai/go-mp4tag"

	ampapi "amdl/internal/amp-api"
	"amdl/internal/download"
	"amdl/internal/fsutil"
)

// coverImageExts are the image types amdl can have saved as cover.<ext>.
var coverImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".tif": true, ".tiff": true, ".bmp": true, ".gif": true,
}

// isCoverImage reports whether name is an album cover file written by amdl
// (cover.jpg, cover.png, ...), matched case-insensitively.
func isCoverImage(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return coverImageExts[ext] && strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), "cover")
}

// trackInfo is what a downloaded track's tags tell about its album.
type trackInfo struct {
	albumID     string // iTunes album ID; only .m4a files carry it
	upc         string
	isrc        string
	album       string
	albumArtist string
}

// playlistAlbumArtist is the album artist amdl tags playlist and station
// tracks with; their folders hold a playlist, not an album.
const playlistAlbumArtist = "Apple Music"

// ArtSpec is an artwork request from coverfix's -cover / -embed flags:
// "jpg" or "png", optionally followed by a resolution ("png5000").
type ArtSpec struct {
	Format string // "jpg" or "png"
	Res    int    // edge length in pixels; 0 = full size
}

var artSpecRe = regexp.MustCompile(`^(?i)(jpg|png)([0-9]+)?$`)

// ParseArtSpec parses "jpg", "png", "jpg3000", "png5000", ...
func ParseArtSpec(s string) (ArtSpec, error) {
	m := artSpecRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ArtSpec{}, fmt.Errorf("%q: want jpg or png, optionally with a resolution, e.g. png or jpg3000", s)
	}
	spec := ArtSpec{Format: strings.ToLower(m[1])}
	if m[2] != "" {
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 100 || n > 10000 {
			return ArtSpec{}, fmt.Errorf("%q: resolution must be between 100 and 10000", s)
		}
		spec.Res = n
	}
	return spec, nil
}

func (a ArtSpec) String() string {
	if a.Res == 0 {
		return a.Format + " full size"
	}
	return fmt.Sprintf("%s %dx%d", a.Format, a.Res, a.Res)
}

func (a ArtSpec) size() string {
	if a.Res == 0 {
		return fullSizeArtwork
	}
	return fmt.Sprintf("%dx%d", a.Res, a.Res)
}

func (a ArtSpec) mime() string {
	if a.Format == "png" {
		return "image/png"
	}
	return "image/jpeg"
}

// url turns an Apple artwork URL template into a request for this spec.
// Apple's image server converts to the format the URL asks for and caps the
// size at the source resolution.
func (a ArtSpec) url(template string) string {
	re := regexp.MustCompile(`\{w\}x\{h\}`)
	parts := re.Split(template, 2)
	if len(parts) != 2 {
		return template
	}
	rest := parts[1]
	if i := strings.LastIndex(rest, "."); i >= 0 {
		rest = rest[:i]
	}
	return parts[0] + a.size() + rest + "." + a.Format
}

// CoverFixOptions controls RefreshCovers.
type CoverFixOptions struct {
	Cover    ArtSpec // the folder's cover.<ext> file
	Embed    ArtSpec // the picture embedded in every track
	DryRun   bool
	Parallel int // albums processed at once (<1 = 1)
}

// CoverFixResult counts what RefreshCovers did.
type CoverFixResult struct {
	Refreshed, Skipped, Failed   int
	TracksEmbedded, TracksFailed int
}

// RefreshCovers walks root and, for every folder holding tracks (.m4a or
// .flac) of one album, saves that album's cover again as cover.<ext> in the
// -cover format, removes the folder's other cover.* images, and replaces the
// picture embedded in each track with one in the -embed format.
//
// The album is found from the tracks' tags: the iTunes album ID (.m4a only),
// else the UPC, else the ISRC of a track, matched against the album name. The
// old covers are only removed after the new one was saved, so a failed
// download never leaves a folder without artwork. With DryRun nothing is
// changed and nothing is looked up.
func (r *Runner) RefreshCovers(roots []string, storefront, token string, opt CoverFixOptions) (CoverFixResult, error) {
	var res CoverFixResult

	// Find the folders first, then process up to opt.Parallel albums at once:
	// one album's lookup and cover download overlap another's file rewrites.
	// Each album's messages are printed as one block, in folder order.
	// Only folders that hold audio files count as albums. The albums of all
	// roots go into one queue, so e.g. several artist folders share the
	// --parallel slots instead of being done one artist after another.
	var dirs []string
	rootOf := map[string]string{} // album folder -> the root it was found in
	seen := map[string]bool{}
	var root string
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Printf("✖ %s: %v\n", path, err)
			res.Failed++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".flac", ".m4a":
			if dir := filepath.Dir(path); !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
				rootOf[dir] = root
			}
		}
		return nil
	}
	var err error
	for _, root = range roots {
		start := len(dirs)
		if werr := filepath.WalkDir(root, walk); werr != nil && err == nil {
			err = werr
		}
		sort.Strings(dirs[start:])
	}
	view := r.newCoverView(len(dirs))
	var failures []string

	type albumDone struct {
		out string
		res CoverFixResult
	}
	n := opt.Parallel
	if n < 1 {
		n = 1
	}
	results := make([]chan albumDone, len(dirs))
	for i := range results {
		results[i] = make(chan albumDone, 1)
	}
	go func() {
		slots := make(chan struct{}, n)
		for i, dir := range dirs {
			slots <- struct{}{}
			go func(i int, dir string) {
				defer func() { <-slots }()
				var out bytes.Buffer
				name := albumLabel(rootOf[dir], dir, len(roots) > 1)
				ar := r.refreshAlbum(&out, dir, storefront, token, opt, view.begin(i, name))
				view.end(i, ar.Failed > 0)
				results[i] <- albumDone{out.String(), ar}
			}(i, dir)
		}
	}()
	for i := range dirs {
		d := <-results[i]
		if view == nil {
			fmt.Print(d.out)
		} else if d.res.Failed > 0 || d.res.TracksFailed > 0 {
			// With the live bars, only problems are printed (after the bars).
			failures = append(failures, d.out)
		}
		res.Refreshed += d.res.Refreshed
		res.Skipped += d.res.Skipped
		res.Failed += d.res.Failed
		res.TracksEmbedded += d.res.TracksEmbedded
		res.TracksFailed += d.res.TracksFailed
	}
	view.finish()
	for _, f := range failures {
		fmt.Print(f)
	}
	return res, err
}

// refreshAlbum refreshes the covers of one folder, writing its messages to w.
// Folders without tracks are left alone. It only reads r.Config, so several
// albums can run at once.
func (r *Runner) refreshAlbum(w io.Writer, dir, storefront, token string, opt CoverFixOptions, p *albumProgress) CoverFixResult {
	var res CoverFixResult
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(w, "✖ %s: %v\n", dir, err)
		res.Failed++
		return res
	}

	var oldCovers, trackPaths []string
	var tracks []trackInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if isCoverImage(name) {
			oldCovers = append(oldCovers, filepath.Join(dir, name))
			continue
		}
		if t, ok := readTrackInfo(filepath.Join(dir, name)); ok {
			tracks = append(tracks, t)
			trackPaths = append(trackPaths, filepath.Join(dir, name))
		}
	}
	if len(tracks) == 0 {
		return res
	}

	if why := notOneAlbum(tracks); why != "" {
		fmt.Fprintf(w, "– %s: %s, skipped\n", dir, why)
		res.Skipped++
		return res
	}

	if opt.DryRun {
		fmt.Fprintf(w, "• %s: would replace %s with cover.%s, and re-embed %d tracks\n", dir, describe(oldCovers), opt.Cover.Format, len(trackPaths))
		res.Refreshed++
		return res
	}

	p.step("looking up the album")
	template, via, err := r.findAlbumArtwork(tracks, storefront, token)
	if err != nil {
		fmt.Fprintf(w, "✖ %s: %v (kept existing covers)\n", dir, err)
		res.Failed++
		return res
	}

	p.step("downloading the cover")
	newCover, err := saveArtwork(filepath.Join(dir, "cover."+opt.Cover.Format), opt.Cover.url(template))
	if err != nil {
		fmt.Fprintf(w, "✖ %s: cover download failed: %v (kept existing covers)\n", dir, err)
		res.Failed++
		return res
	}
	for _, old := range oldCovers {
		if filepath.Clean(old) == filepath.Clean(newCover) {
			continue
		}
		if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(w, "  could not remove %s: %v\n", old, err)
		}
	}

	var embedImg []byte
	if opt.Embed == opt.Cover {
		embedImg, err = os.ReadFile(newCover)
	} else {
		embedImg, err = fetchArtwork(opt.Embed.url(template))
	}
	if err != nil {
		fmt.Fprintf(w, "✖ %s: cover saved, but embed image download failed: %v\n", dir, err)
		res.Failed++
		return res
	}
	embedded, failed := 0, 0
	// FLAC can hold at most a 16 MB picture. When it is too large, the
	// track is retried with a smaller PNG (see pngRetrySteps); the size
	// that fits is then used for the album's other FLAC tracks too.
	flacImg, flacSpec := embedImg, opt.Embed
	p.step("embedding")
	p.tracks(0, len(trackPaths))
	for _, tp := range trackPaths {
		img, spec := embedImg, opt.Embed
		if strings.EqualFold(filepath.Ext(tp), ".flac") {
			img, spec = flacImg, flacSpec
		}
		err := embedPicture(tp, img, spec.mime())
		var tooLarge *FLACPictureTooLargeError
		for errors.As(err, &tooLarge) && spec.Format == "png" {
			step := nextPNGStep(tooLarge.Width)
			if step == 0 {
				break
			}
			fmt.Fprintf(w, "  %s: %v; retrying with png %dx%d\n", filepath.Base(tp), err, step, step)
			spec = ArtSpec{Format: "png", Res: step}
			p.step(fmt.Sprintf("retrying with png %dx%d", step, step))
			smaller, dlErr := fetchArtwork(spec.url(template))
			if dlErr != nil {
				err = fmt.Errorf("download png %dx%d: %w", step, step, dlErr)
				break
			}
			img = smaller
			err = embedPicture(tp, img, spec.mime())
			if err == nil {
				flacImg, flacSpec = img, spec
			}
		}
		if err != nil {
			fmt.Fprintf(w, "  could not embed into %s: %v\n", filepath.Base(tp), err)
			failed++
			p.tracks(embedded+failed, len(trackPaths))
			continue
		}
		embedded++
		p.tracks(embedded+failed, len(trackPaths))
		p.step("embedding")
	}
	if flacSpec != opt.Embed {
		fmt.Fprintf(w, "  FLAC tracks embedded with %s (%s was too large for FLAC)\n", flacSpec, opt.Embed)
	}
	res.TracksEmbedded += embedded
	res.TracksFailed += failed

	fmt.Fprintf(w, "✔ %s: %s, embedded in %d/%d tracks (album found by %s)\n", dir, filepath.Base(newCover), embedded, len(trackPaths), via)
	res.Refreshed++
	return res
}

// albumLabel names an album folder for the progress bars: relative to the
// root it was found in, or to the root's parent (so the artist shows) when
// the root is the album itself or several roots were given.
func albumLabel(root, dir string, manyRoots bool) string {
	base := root
	if manyRoots || filepath.Clean(root) == filepath.Clean(dir) {
		base = filepath.Dir(root)
	}
	if rel, err := filepath.Rel(base, dir); err == nil && rel != "." {
		return filepath.ToSlash(rel)
	}
	return dir
}

// pngRetrySteps are the PNG sizes tried, largest first, when a picture is too
// large to embed in a FLAC file.
var pngRetrySteps = []int{5000, 4000, 3000, 2000, 1000}

// nextPNGStep returns the largest retry size below the width of the image that
// was too large, or 0 when there is none. Apple caps sizes at the source
// resolution, so a step at or above that width would return the same image.
func nextPNGStep(width int) int {
	for _, s := range pngRetrySteps {
		if s < width {
			return s
		}
	}
	return 0
}

// fetchArtwork downloads an artwork URL into memory.
func fetchArtwork(url string) ([]byte, error) {
	resp, err := download.GetWithHeaders(url, map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// saveArtwork downloads url to path, replacing it atomically.
func saveArtwork(path, url string) (string, error) {
	img, err := fetchArtwork(url)
	if err != nil {
		return "", err
	}
	tmp, err := fsutil.CreateOutputTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(img); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return path, nil
}

// embedPicture replaces the embedded picture of an .m4a or .flac track.
func embedPicture(path string, img []byte, mime string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		return setFLACPicture(path, img, mime)
	case ".m4a":
		f, err := mp4tag.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		// The library appends pictures, so the old ones are deleted first.
		return f.Write(&mp4tag.MP4Tags{Pictures: []*mp4tag.MP4Picture{{Format: mp4tag.ImageTypeAuto, Data: img}}}, []string{"allpictures"})
	}
	return fmt.Errorf("unsupported file type")
}

// notOneAlbum explains why a folder's tracks are not a single album, or
// returns "" when they are.
func notOneAlbum(tracks []trackInfo) string {
	ids, upcs, names := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, t := range tracks {
		if t.albumArtist == playlistAlbumArtist {
			return "tracks are tagged as a playlist"
		}
		if t.albumID != "" {
			ids[t.albumID] = true
		}
		if t.upc != "" {
			upcs[t.upc] = true
		}
		names[strings.ToLower(t.album+"\x00"+t.albumArtist)] = true
	}
	switch {
	case len(ids) > 1:
		return fmt.Sprintf("tracks from %d different albums", len(ids))
	case len(upcs) > 1:
		return fmt.Sprintf("tracks from %d different albums", len(upcs))
	case len(ids) == 0 && len(upcs) == 0 && len(names) > 1:
		return fmt.Sprintf("tracks from %d different albums", len(names))
	}
	return ""
}

// findAlbumArtwork returns the artwork URL of the folder's album and how the
// album was found.
func (r *Runner) findAlbumArtwork(tracks []trackInfo, storefront, token string) (artURL, via string, err error) {
	lang := r.Config.General.Language
	var lastErr error

	for _, t := range tracks {
		if t.albumID == "" {
			continue
		}
		meta, err := ampapi.GetAlbumResp(storefront, t.albumID, lang, token)
		if err == nil && len(meta.Data) > 0 && meta.Data[0].Attributes.Artwork.URL != "" {
			return meta.Data[0].Attributes.Artwork.URL, "album ID " + t.albumID, nil
		}
		lastErr = fmt.Errorf("album ID %s: %v", t.albumID, errOrEmpty(err))
		break
	}

	for _, t := range tracks {
		if t.upc == "" {
			continue
		}
		albums, err := ampapi.GetAlbumsByUPC(storefront, t.upc, lang, token)
		if err == nil && len(albums) > 0 && albums[0].Attributes.Artwork.URL != "" {
			return albums[0].Attributes.Artwork.URL, "UPC " + t.upc, nil
		}
		lastErr = fmt.Errorf("UPC %s: %v", t.upc, errOrEmpty(err))
		break
	}

	// ISRC identifies a recording, which is often on several albums, so only
	// accept a song whose album name matches the track's album tag.
	tried := 0
	for _, t := range tracks {
		if t.isrc == "" || t.album == "" {
			continue
		}
		if tried == 3 {
			break
		}
		tried++
		songs, err := ampapi.GetSongsByISRC(storefront, t.isrc, lang, token)
		if err != nil {
			lastErr = fmt.Errorf("ISRC %s: %v", t.isrc, err)
			continue
		}
		for _, s := range songs {
			if strings.EqualFold(strings.TrimSpace(s.Attributes.AlbumName), strings.TrimSpace(t.album)) && s.Attributes.Artwork.URL != "" {
				return s.Attributes.Artwork.URL, "ISRC " + t.isrc, nil
			}
		}
		lastErr = fmt.Errorf("ISRC %s: no song on an album named %q", t.isrc, t.album)
	}

	if lastErr == nil {
		lastErr = errors.New("tracks have no album ID, UPC or ISRC tag")
	}
	return "", "", fmt.Errorf("album not found: %w", lastErr)
}

func errOrEmpty(err error) error {
	if err != nil {
		return err
	}
	return errors.New("no artwork in response")
}

// readTrackInfo reads the album identifiers from an .m4a or .flac file.
func readTrackInfo(path string) (trackInfo, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a":
		f, err := mp4tag.Open(path)
		if err != nil {
			return trackInfo{}, false
		}
		defer f.Close()
		tags, err := f.Read()
		if err != nil {
			return trackInfo{}, false
		}
		t := trackInfo{album: tags.Album, albumArtist: tags.AlbumArtist}
		if tags.ItunesAlbumID != 0 {
			// amdl stores the ID as int32; read it back unsigned so IDs
			// above 2^31 are not turned negative.
			t.albumID = strconv.FormatUint(uint64(uint32(tags.ItunesAlbumID)), 10)
		}
		for k, v := range tags.Custom {
			switch strings.ToUpper(k) {
			case "UPC":
				t.upc = strings.TrimSpace(v)
			case "ISRC":
				t.isrc = strings.TrimSpace(v)
			}
		}
		return t, true
	case ".flac":
		tags, err := readFLACTags(path)
		if err != nil {
			return trackInfo{}, false
		}
		albumArtist := tags["ALBUM_ARTIST"]
		if albumArtist == "" {
			albumArtist = tags["ALBUMARTIST"]
		}
		return trackInfo{
			upc:         strings.TrimSpace(tags["UPC"]),
			isrc:        strings.TrimSpace(tags["ISRC"]),
			album:       tags["ALBUM"],
			albumArtist: albumArtist,
		}, true
	}
	return trackInfo{}, false
}

func describe(paths []string) string {
	if len(paths) == 0 {
		return "(no existing cover)"
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
