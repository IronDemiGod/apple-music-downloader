package ampapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"amdl/internal/download"
)

// CatalogItem is the part of a catalog album or song that cover lookups need.
type CatalogItem struct {
	ID         string `json:"id"`
	Attributes struct {
		Name       string `json:"name"`
		ArtistName string `json:"artistName"`
		AlbumName  string `json:"albumName"`
		Artwork    struct {
			URL string `json:"url"`
		} `json:"artwork"`
	} `json:"attributes"`
}

// GetAlbumsByUPC returns the catalog albums with the given UPC.
func GetAlbumsByUPC(storefront, upc, language, token string) ([]CatalogItem, error) {
	return catalogFilter(storefront, "albums", "upc", upc, language, token)
}

// GetSongsByISRC returns the catalog songs with the given ISRC. One recording
// is often on several albums (originals, compilations, deluxe editions).
func GetSongsByISRC(storefront, isrc, language, token string) ([]CatalogItem, error) {
	return catalogFilter(storefront, "songs", "isrc", isrc, language, token)
}

func catalogFilter(storefront, kind, field, value, language, token string) ([]CatalogItem, error) {
	q := url.Values{}
	q.Set("filter["+field+"]", value)
	if language != "" {
		q.Set("l", language)
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s/%s?%s", storefront, kind, q.Encode()), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Origin", "https://music.apple.com")
	resp, err := download.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	var out struct {
		Data []CatalogItem `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
