package app

import "testing"

func TestIsCoverImage(t *testing.T) {
	yes := []string{"cover.jpg", "cover.png", "Cover.JPG", "cover.jpeg", "cover.webp"}
	no := []string{"folder.jpg", "cover.m4a", "cover", "cover.tmp-123", "01. cover.jpg", "back cover.png"}
	for _, n := range yes {
		if !isCoverImage(n) {
			t.Errorf("isCoverImage(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if isCoverImage(n) {
			t.Errorf("isCoverImage(%q) = true, want false", n)
		}
	}
}

func vorbisBlock(vendor string, comments ...string) []byte {
	var b []byte
	put := func(s string) {
		n := len(s)
		b = append(b, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
		b = append(b, s...)
	}
	put(vendor)
	n := len(comments)
	b = append(b, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
	for _, c := range comments {
		put(c)
	}
	return b
}

func TestParseVorbisComments(t *testing.T) {
	tags, err := parseVorbisComments(vorbisBlock("Lavf", "album=Whenever You Need Somebody", "UPC=4050538826562", "isrc=GB5KW2202504", "noequals"))
	if err != nil {
		t.Fatal(err)
	}
	if tags["ALBUM"] != "Whenever You Need Somebody" || tags["UPC"] != "4050538826562" || tags["ISRC"] != "GB5KW2202504" {
		t.Fatalf("tags = %v", tags)
	}
	if _, err := parseVorbisComments(vorbisBlock("x", "a=b")[:10]); err == nil {
		t.Fatal("truncated block: want error")
	}
}

func TestNotOneAlbum(t *testing.T) {
	album := trackInfo{upc: "1", album: "A", albumArtist: "X"}
	tests := []struct {
		name   string
		tracks []trackInfo
		one    bool
	}{
		{"same UPC", []trackInfo{album, album}, true},
		{"different UPCs", []trackInfo{album, {upc: "2", album: "B", albumArtist: "Y"}}, false},
		{"different album IDs", []trackInfo{{albumID: "1"}, {albumID: "2"}}, false},
		{"flac and m4a of one album", []trackInfo{{albumID: "9", upc: "1"}, album}, true},
		{"no IDs, same album name", []trackInfo{{isrc: "a", album: "A"}, {isrc: "b", album: "A"}}, true},
		{"no IDs, different album names", []trackInfo{{isrc: "a", album: "A"}, {isrc: "b", album: "B"}}, false},
		{"playlist folder", []trackInfo{{isrc: "a", album: "P", albumArtist: "Apple Music"}}, false},
	}
	for _, tt := range tests {
		if got := notOneAlbum(tt.tracks) == ""; got != tt.one {
			t.Errorf("%s: one album = %v, want %v", tt.name, got, tt.one)
		}
	}
}

func TestPNGArtworkURL(t *testing.T) {
	tests := map[string]string{
		"https://is1-ssl.mzstatic.com/image/thumb/Music122/v4/de/eb/63/x/4050538826562.jpg/{w}x{h}bb.jpg": "https://is1-ssl.mzstatic.com/image/thumb/Music122/v4/de/eb/63/x/4050538826562.jpg/10000x10000bb.png",
		"https://is1-ssl.mzstatic.com/image/thumb/Music/y/191404113974.png/{w}x{h}bb.webp":                "https://is1-ssl.mzstatic.com/image/thumb/Music/y/191404113974.png/10000x10000bb.png",
	}
	for in, want := range tests {
		if got := pngArtworkURL(in, fullSizeArtwork); got != want {
			t.Errorf("pngArtworkURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseArtSpec(t *testing.T) {
	good := map[string]ArtSpec{
		"png": {"png", 0}, "jpg": {"jpg", 0}, "PNG5000": {"png", 5000}, "jpg3000": {"jpg", 3000},
	}
	for in, want := range good {
		got, err := ParseArtSpec(in)
		if err != nil || got != want {
			t.Errorf("ParseArtSpec(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "webp", "png50", "png20000", "png-5000", "5000", "pngx"} {
		if _, err := ParseArtSpec(in); err == nil {
			t.Errorf("ParseArtSpec(%q) = nil error, want error", in)
		}
	}
	tmpl := "https://is1-ssl.mzstatic.com/image/thumb/M/x/1.jpg/{w}x{h}bb.jpg"
	if got := (ArtSpec{"png", 0}).url(tmpl); got != "https://is1-ssl.mzstatic.com/image/thumb/M/x/1.jpg/10000x10000bb.png" {
		t.Errorf("png full url = %s", got)
	}
	if got := (ArtSpec{"jpg", 3000}).url(tmpl); got != "https://is1-ssl.mzstatic.com/image/thumb/M/x/1.jpg/3000x3000bb.jpg" {
		t.Errorf("jpg3000 url = %s", got)
	}
}
