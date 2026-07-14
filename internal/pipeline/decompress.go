package pipeline

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// maxDecompressed caps expansion of compressed feed bodies so a
// hostile archive cannot balloon memory (the fetch layer already caps
// the wire size).
const maxDecompressed = 512 << 20

var errDecompressedTooLarge = errors.New("decompressed body exceeds size cap")

// decompress unwraps gzip and zip feed bodies (several catalog feeds
// ship compressed; pfBlockerNG unwrapped them transparently and so do
// we). Plain bodies pass through untouched. For zip archives the
// largest regular file wins: real-world feed zips hold one list plus
// the occasional readme.
func decompress(body []byte) ([]byte, error) {
	switch {
	case len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b:
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = zr.Close() }()
		out, err := readCapped(zr)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		return out, nil

	case bytes.HasPrefix(body, []byte("PK\x03\x04")):
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, fmt.Errorf("zip: %w", err)
		}
		var pick *zip.File
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if pick == nil || f.UncompressedSize64 > pick.UncompressedSize64 {
				pick = f
			}
		}
		if pick == nil {
			return nil, errors.New("zip: no regular files in archive")
		}
		rc, err := pick.Open()
		if err != nil {
			return nil, fmt.Errorf("zip %s: %w", pick.Name, err)
		}
		defer func() { _ = rc.Close() }()
		out, err := readCapped(rc)
		if err != nil {
			return nil, fmt.Errorf("zip %s: %w", pick.Name, err)
		}
		return out, nil
	}
	return body, nil
}

func readCapped(r io.Reader) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, maxDecompressed+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecompressed {
		return nil, errDecompressedTooLarge
	}
	return out, nil
}
