package app

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders for image.DecodeConfig
	_ "image/png"
	"io"
	"os"
	"path/filepath"

	"amdl/internal/fsutil"
)

const (
	flacBlockPadding = 1
	flacBlockPicture = 6
	// flacMaxBlock is the largest metadata block FLAC can store (24-bit length).
	flacMaxBlock = 1<<24 - 1
)

// flacPictureLimit is flacMaxBlock; tests lower it.
var flacPictureLimit = flacMaxBlock

// setFLACPicture replaces every embedded picture of a FLAC file with img as
// the front cover. Only the metadata blocks are rewritten; the audio frames
// are copied byte for byte. The file is replaced atomically.
func setFLACPicture(path string, img []byte, mime string) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return fmt.Errorf("read image size: %w", err)
	}
	depth := 24
	if mime == "image/png" {
		depth = 32
	}
	var pic bytes.Buffer
	put := func(v uint32) { _ = binary.Write(&pic, binary.BigEndian, v) }
	put(3) // front cover
	put(uint32(len(mime)))
	pic.WriteString(mime)
	put(0) // description length
	put(uint32(cfg.Width))
	put(uint32(cfg.Height))
	put(uint32(depth))
	put(0) // colors (non-indexed)
	put(uint32(len(img)))
	pic.Write(img)
	if pic.Len() > flacPictureLimit {
		return flacTooLarge(cfg.Width, cfg.Height, mime, len(img))
	}

	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	r := bufio.NewReaderSize(in, 1<<16)

	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "fLaC" {
		return errors.New("not a FLAC file")
	}
	type block struct {
		typ  byte
		data []byte
	}
	var blocks []block
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return fmt.Errorf("read metadata: %w", err)
		}
		last := hdr[0]&0x80 != 0
		typ := hdr[0] & 0x7f
		size := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return fmt.Errorf("read metadata: %w", err)
		}
		if typ != flacBlockPicture {
			blocks = append(blocks, block{typ, data})
		}
		if last {
			break
		}
	}
	if len(blocks) == 0 || blocks[0].typ != 0 {
		return errors.New("FLAC file has no STREAMINFO block")
	}

	// New picture goes before any padding, which stays last.
	var out []block
	inserted := false
	for _, b := range blocks {
		if b.typ == flacBlockPadding && !inserted {
			out = append(out, block{flacBlockPicture, pic.Bytes()})
			inserted = true
		}
		out = append(out, b)
	}
	if !inserted {
		out = append(out, block{flacBlockPicture, pic.Bytes()})
	}

	tmp, err := fsutil.CreateOutputTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()
	w := bufio.NewWriterSize(tmp, 1<<16)
	w.WriteString("fLaC")
	for i, b := range out {
		h := b.typ
		if i == len(out)-1 {
			h |= 0x80
		}
		n := len(b.data)
		w.Write([]byte{h, byte(n >> 16), byte(n >> 8), byte(n)})
		w.Write(b.data)
	}
	if _, err := io.Copy(w, r); err != nil { // audio frames, untouched
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Chmod(st.Mode().Perm()); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := in.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// FLACPictureTooLargeError reports a picture that does not fit in one FLAC
// metadata block, whose length field is 24 bits wide (at most 16 MB).
type FLACPictureTooLargeError struct {
	Width, Height int
	Format        string // "PNG" or "JPEG"
	Size          int    // bytes
}

func (e *FLACPictureTooLargeError) Error() string {
	return fmt.Sprintf("image is %dx%d %s, %.1f MB; FLAC can embed at most %.1f MB per picture",
		e.Width, e.Height, e.Format, float64(e.Size)/(1<<20), float64(flacPictureLimit)/(1<<20))
}

func flacTooLarge(width, height int, mime string, size int) error {
	format := "JPEG"
	if mime == "image/png" {
		format = "PNG"
	}
	return &FLACPictureTooLargeError{Width: width, Height: height, Format: format, Size: size}
}
