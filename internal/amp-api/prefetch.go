package ampapi

import "sync"

// Albums fetched ahead of time (a playlist sorted into album folders fetches
// its next albums while the current one downloads). GetAlbumResp returns a
// prefetched album once, then forgets it.
var prefetchedAlbums sync.Map

func albumKey(storefront, id, language string) string {
	return storefront + "|" + id + "|" + language
}

// PrefetchAlbum fetches an album so that the next GetAlbumResp for it
// returns at once.
func PrefetchAlbum(storefront, id, language, token string) (*AlbumResp, error) {
	resp, err := GetAlbumResp(storefront, id, language, token)
	if err == nil {
		prefetchedAlbums.Store(albumKey(storefront, id, language), resp)
	}
	return resp, err
}

// DropPrefetchedAlbum forgets a prefetched album that was not used.
func DropPrefetchedAlbum(storefront, id, language string) {
	prefetchedAlbums.Delete(albumKey(storefront, id, language))
}
