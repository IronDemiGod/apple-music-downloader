package app

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	ampapi "amdl/internal/amp-api"
)

// Prefetching for playlists sorted into album folders: each album's details
// and cover must be fetched before its tracks are queued, and with
// artwork "original" the cover is a full-size PNG of 5-40 MB that Apple's
// server renders on request. A playlist of one-song albums then queued a
// track only every few seconds, so its downloads ran one at a time. A few
// workers now fetch the next albums and covers (covers only for album folders
// that have none yet) while the current ones download.

// prefetchAhead is how many albums may be fetched ahead of the one being
// queued (it bounds the covers held in the work folder).
const prefetchAhead = 12

// coverCache holds downloaded covers by URL until they are used. The first
// caller of a URL downloads it; others wait for that download.
type coverCache struct {
	dir string
	mu  sync.Mutex
	m   map[string]*coverEntry
	n   int
}

type coverEntry struct {
	ready chan struct{}
	path  string
	err   error
}

// get returns the cached download of url, downloading it if needed. take
// removes it from the cache: the caller copies it and it is not needed again.
func (c *coverCache) get(url string, take bool, download func(*os.File) error) *coverEntry {
	c.mu.Lock()
	e, ok := c.m[url]
	if !ok {
		e = &coverEntry{ready: make(chan struct{})}
		c.m[url] = e
		c.n++
		e.path = filepath.Join(c.dir, "cover-"+strconv.Itoa(c.n))
	}
	c.mu.Unlock()
	if !ok {
		f, err := os.Create(e.path)
		if err == nil {
			err = download(f)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		e.err = err
		close(e.ready)
	}
	<-e.ready
	if take {
		c.mu.Lock()
		if c.m[url] == e {
			delete(c.m, url)
		}
		c.mu.Unlock()
	}
	return e
}

// cachedCover serves getCover from the cache: it copies the cached download
// of url to out.
func (r *Runner) cachedCover(url string, out *os.File) error {
	e := r.covers.get(url, !r.prefetching, func(f *os.File) error { return downloadCover(url, f) })
	if !r.prefetching {
		defer os.Remove(e.path)
	}
	if e.err != nil {
		return e.err
	}
	if r.prefetching {
		return nil // the real call copies it
	}
	in, err := os.Open(e.path)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := out.Truncate(0); err != nil {
		return err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return err
}

// albumPrefetcher fetches albums (and their covers) ahead of ripAlbum.
type albumPrefetcher struct {
	r       *Runner
	dir     string
	next    atomic.Int64 // index of the album ripAlbum is at
	tickets chan struct{}
	wg      sync.WaitGroup
}

// startAlbumPrefetch starts fetching the albums in order, at most
// prefetchAhead ahead of done() calls. nil when there is nothing to gain.
func (r *Runner) startAlbumPrefetch(albumIDs []string, storefront, token string) *albumPrefetcher {
	if len(albumIDs) < 2 {
		return nil
	}
	base := r.workRunDir()
	dir, err := os.MkdirTemp(base, "prefetch-")
	if err != nil {
		return nil
	}
	p := &albumPrefetcher{r: r, dir: dir, tickets: make(chan struct{}, len(albumIDs)+prefetchAhead)}
	r.covers = &coverCache{dir: dir, m: map[string]*coverEntry{}}
	for i := 0; i < prefetchAhead; i++ {
		p.tickets <- struct{}{}
	}
	// A runner of its own for the workers: it only reads the config, and its
	// cover downloads stay in the cache for the real calls.
	pr := &Runner{Config: r.Config, Flags: r.Flags, covers: r.covers, prefetching: true}
	ids := make(chan int)
	go func() {
		defer close(ids)
		for i := range albumIDs {
			ids <- i
		}
	}()
	lang := r.Config.General.Language
	for w := 0; w < 6; w++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for i := range ids {
				<-p.tickets
				if int64(i) < p.next.Load() {
					continue // ripAlbum got there first
				}
				resp, err := ampapi.PrefetchAlbum(storefront, albumIDs[i], lang, token)
				if err != nil || len(resp.Data) == 0 {
					continue
				}
				// An album folder that has its cover keeps it (see ripAlbum): no
				// download. Known without the network unless the album folder
				// name uses {Quality}.
				if !strings.Contains(r.Config.Paths.AlbumFolder, "Quality") {
					_, _, singerFolder, albumFolderName := pr.albumFolderNames(*resp, albumIDs[i], storefront, lang, token)
					if pr.existingCover(collectionFolderPath(singerFolder, albumFolderName), "cover", resp.Data[0].Attributes.Artwork.URL) != "" {
						continue
					}
				}
				scratch := filepath.Join(dir, "album-"+strconv.Itoa(i))
				if os.MkdirAll(scratch, 0o700) == nil {
					pr.writeCoverFrom(scratch, "cover", resp.Data[0].Attributes.Artwork.URL)
					os.RemoveAll(scratch)
				}
			}
		}()
	}
	return p
}

// done tells the prefetcher that album i has been queued.
func (p *albumPrefetcher) done(i int) {
	if p == nil {
		return
	}
	p.next.Store(int64(i + 1))
	p.tickets <- struct{}{}
}

// stop waits for the workers and drops what was not used.
func (p *albumPrefetcher) stop(albumIDs []string, storefront string) {
	if p == nil {
		return
	}
	p.next.Store(int64(len(albumIDs)))
	for range albumIDs { // let blocked workers through; they skip
		select {
		case p.tickets <- struct{}{}:
		default:
		}
	}
	p.wg.Wait()
	for _, id := range albumIDs {
		ampapi.DropPrefetchedAlbum(storefront, id, p.r.Config.General.Language)
	}
	p.r.covers = nil
	os.RemoveAll(p.dir)
}
