package app

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func flacBlock(typ byte, last bool, data []byte) []byte {
	h := typ
	if last {
		h |= 0x80
	}
	n := len(data)
	return append([]byte{h, byte(n >> 16), byte(n >> 8), byte(n)}, data...)
}

func TestSetFLACPicture(t *testing.T) {
	audio := bytes.Repeat([]byte{0xff, 0xf8, 0x12, 0x34}, 1000)
	vorbis := vorbisBlock("test", "ALBUM=A", "UPC=1")
	var f []byte
	f = append(f, "fLaC"...)
	f = append(f, flacBlock(0, false, make([]byte, 34))...)           // STREAMINFO
	f = append(f, flacBlock(4, false, vorbis)...)                     // tags
	f = append(f, flacBlock(6, false, []byte("old picture data"))...) // old picture
	f = append(f, flacBlock(1, true, make([]byte, 100))...)           // padding
	f = append(f, audio...)
	path := filepath.Join(t.TempDir(), "t.flac")
	if err := os.WriteFile(path, f, 0o644); err != nil {
		t.Fatal(err)
	}

	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 7, 5))); err != nil {
		t.Fatal(err)
	}
	if err := setFLACPicture(path, img.Bytes(), "image/png"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(got, audio) {
		t.Fatal("audio frames changed")
	}
	if tags, err := readFLACTags(path); err != nil || tags["UPC"] != "1" {
		t.Fatalf("tags after rewrite = %v, %v", tags, err)
	}
	// Walk the blocks: STREAMINFO, VORBIS, PICTURE (new), PADDING (last).
	p := got[4:]
	var types []byte
	var picture []byte
	for {
		typ, last := p[0]&0x7f, p[0]&0x80 != 0
		n := int(p[1])<<16 | int(p[2])<<8 | int(p[3])
		types = append(types, typ)
		if typ == 6 {
			picture = p[4 : 4+n]
		}
		p = p[4+n:]
		if last {
			break
		}
	}
	if !bytes.Equal(types, []byte{0, 4, 6, 1}) {
		t.Fatalf("block order = %v, want [0 4 6 1]", types)
	}
	if !bytes.HasSuffix(picture, img.Bytes()) || !bytes.Contains(picture, []byte("image/png")) {
		t.Fatal("new picture block does not hold the image")
	}
	if !bytes.Equal(p, audio) {
		t.Fatal("bytes after metadata are not the original audio")
	}
}

func TestSetFLACPictureTooLarge(t *testing.T) {
	var f []byte
	f = append(f, "fLaC"...)
	f = append(f, flacBlock(0, true, make([]byte, 34))...)
	f = append(f, 0xff, 0xf8)
	path := filepath.Join(t.TempDir(), "t.flac")
	if err := os.WriteFile(path, f, 0o644); err != nil {
		t.Fatal(err)
	}
	// An uncompressed 2100x2100 RGBA PNG is about 17.6 MB.
	var img bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2100, 2100))); err != nil {
		t.Fatal(err)
	}
	err := setFLACPicture(path, img.Bytes(), "image/png")
	if err == nil {
		t.Fatal("want too-large error")
	}
	t.Log(err)
	var tooLarge *FLACPictureTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Width != 2100 {
		t.Fatalf("error %v is not a FLACPictureTooLargeError for width 2100", err)
	}
	for _, want := range []string{"2100x2100 PNG", "16.0 MB"} {
		if !bytes.Contains([]byte(err.Error()), []byte(want)) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, f) {
		t.Fatal("file changed although the picture was rejected")
	}
}

func TestNextPNGStep(t *testing.T) {
	for width, want := range map[int]int{10000: 5000, 6000: 5000, 5000: 4000, 4000: 3000, 3000: 2000, 2000: 1000, 1000: 0, 600: 0} {
		if got := nextPNGStep(width); got != want {
			t.Errorf("nextPNGStep(%d) = %d, want %d", width, got, want)
		}
	}
}

// flacHasPicture recognises a file that already has exactly the picture
// (after setFLACPicture) and rejects other pictures, so coverfix can skip
// rewriting unchanged tracks.
func TestFLACHasPicture(t *testing.T) {
	var f []byte
	f = append(f, "fLaC"...)
	f = append(f, flacBlock(0, false, make([]byte, 34))...)
	f = append(f, flacBlock(6, false, []byte("old picture data"))...)
	f = append(f, flacBlock(1, true, make([]byte, 10))...)
	f = append(f, bytes.Repeat([]byte{0xff, 0xf8}, 100)...)
	path := filepath.Join(t.TempDir(), "t.flac")
	if err := os.WriteFile(path, f, 0o644); err != nil {
		t.Fatal(err)
	}
	var a, b bytes.Buffer
	png.Encode(&a, image.NewRGBA(image.Rect(0, 0, 7, 5)))
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 5)))

	if ok, err := flacHasPicture(path, a.Bytes()); err != nil || ok {
		t.Fatalf("before embedding: %v, %v; want false", ok, err)
	}
	if err := setFLACPicture(path, a.Bytes(), "image/png"); err != nil {
		t.Fatal(err)
	}
	if ok, err := flacHasPicture(path, a.Bytes()); err != nil || !ok {
		t.Fatalf("same picture: %v, %v; want true", ok, err)
	}
	if ok, _ := flacHasPicture(path, b.Bytes()); ok {
		t.Fatal("a different picture must not match")
	}
	if !alreadyEmbedded(path, a.Bytes()) || alreadyEmbedded(path, b.Bytes()) {
		t.Fatal("alreadyEmbedded disagrees with flacHasPicture")
	}
}
