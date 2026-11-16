package app

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
)

// readFLACTags returns the Vorbis comments of a FLAC file, keyed by upper-case
// field name (Vorbis comment names are case-insensitive).
func readFLACTags(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)

	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, err
	}
	if string(magic) != "fLaC" {
		return nil, errors.New("not a FLAC file")
	}
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, err
		}
		last := hdr[0]&0x80 != 0
		blockType := hdr[0] & 0x7f
		size := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
		if blockType == 4 { // VORBIS_COMMENT
			block := make([]byte, size)
			if _, err := io.ReadFull(r, block); err != nil {
				return nil, err
			}
			return parseVorbisComments(block)
		}
		if _, err := r.Discard(size); err != nil {
			return nil, err
		}
		if last {
			return map[string]string{}, nil
		}
	}
}

func parseVorbisComments(b []byte) (map[string]string, error) {
	errShort := errors.New("truncated vorbis comment block")
	next := func() (string, error) {
		if len(b) < 4 {
			return "", errShort
		}
		n := binary.LittleEndian.Uint32(b)
		b = b[4:]
		if uint64(n) > uint64(len(b)) {
			return "", errShort
		}
		s := string(b[:n])
		b = b[n:]
		return s, nil
	}
	if _, err := next(); err != nil { // vendor string
		return nil, err
	}
	if len(b) < 4 {
		return nil, errShort
	}
	count := binary.LittleEndian.Uint32(b)
	b = b[4:]
	tags := map[string]string{}
	for i := uint32(0); i < count; i++ {
		c, err := next()
		if err != nil {
			return nil, err
		}
		if k, v, ok := strings.Cut(c, "="); ok {
			k = strings.ToUpper(k)
			if _, dup := tags[k]; !dup {
				tags[k] = v
			}
		}
	}
	return tags, nil
}
