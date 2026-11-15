package session

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A bundle is what one store entry carries: a gzipped tar holding the
// transcript and the companion directory's files.
const (
	transcriptEntry = "session.jsonl"
	companionPrefix = "companion/"
)

// maxEntry bounds one file in a bundle. Bundles come from other machines;
// the size is checked before allocating.
const maxEntry = 4 << 30

// Contents is an unpacked bundle.
type Contents struct {
	Transcript []byte
	Companion  map[string][]byte // slash-separated path relative to the companion dir
}

// ReadContents reads a local session's transcript and companion files.
func ReadContents(s Session) (Contents, error) {
	c := Contents{Companion: map[string][]byte{}}
	var err error
	if c.Transcript, err = os.ReadFile(s.File); err != nil {
		return Contents{}, err
	}
	err = walkCompanion(s.Dir, func(rel string, _ os.FileInfo) error {
		data, err := os.ReadFile(filepath.Join(s.Dir, filepath.FromSlash(rel)))
		c.Companion[rel] = data
		return err
	})
	return c, err
}

// Pack builds a bundle from contents. The hash covers the uncompressed
// tar, which is deterministic (sorted names, fixed modes and times), so
// unchanged contents always hash the same.
func Pack(c Contents) (payload []byte, hash string, err error) {
	// The tar streams through the hash and gzip at once: transcripts can
	// be hundreds of megabytes, and a full uncompressed copy isn't needed.
	var gz bytes.Buffer
	h := sha256.New()
	zw := gzip.NewWriter(&gz)
	tw := tar.NewWriter(io.MultiWriter(h, zw))
	add := func(name string, data []byte) error {
		hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)),
			Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := add(transcriptEntry, c.Transcript); err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(c.Companion))
	for n := range c.Companion {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := add(companionPrefix+n, c.Companion[n]); err != nil {
			return nil, "", err
		}
	}
	if err := errors.Join(tw.Close(), zw.Close()); err != nil {
		return nil, "", err
	}
	return gz.Bytes(), hex.EncodeToString(h.Sum(nil)), nil
}

// Unpack validates and unpacks a bundle. Bundles come from the store, which
// other machines write, so every name is checked before it can reach a
// filesystem path.
func Unpack(payload io.Reader) (*Contents, error) {
	zr, err := gzip.NewReader(payload)
	if err != nil {
		return nil, fmt.Errorf("bad bundle: %w", err)
	}
	tr := tar.NewReader(zr)
	c := &Contents{Companion: map[string][]byte{}}
	seenTranscript := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bad bundle: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("bad bundle: %q is not a regular file", hdr.Name)
		}
		if hdr.Size < 0 || hdr.Size > maxEntry {
			return nil, fmt.Errorf("bad bundle: %q has size %d", hdr.Name, hdr.Size)
		}
		// Sized up front: growing a buffer to a large transcript would
		// briefly need twice its size.
		data := make([]byte, hdr.Size)
		if _, err := io.ReadFull(tr, data); err != nil {
			return nil, fmt.Errorf("bad bundle: %w", err)
		}
		switch {
		case hdr.Name == transcriptEntry && !seenTranscript:
			c.Transcript, seenTranscript = data, true
		case strings.HasPrefix(hdr.Name, companionPrefix):
			rel := strings.TrimPrefix(hdr.Name, companionPrefix)
			if !safeRel(rel) {
				return nil, fmt.Errorf("bad bundle: unsafe path %q", hdr.Name)
			}
			if _, dup := c.Companion[rel]; dup {
				return nil, fmt.Errorf("bad bundle: duplicate %q", hdr.Name)
			}
			c.Companion[rel] = data
		default:
			// Written by a newer ccsync (e.g. a new kind of session state):
			// skip it rather than refuse the whole session.
		}
	}
	if !seenTranscript {
		return nil, fmt.Errorf("bad bundle: no transcript")
	}
	return c, nil
}

// safeRel accepts clean, relative, slash-separated paths that stay inside
// their root on every OS.
func safeRel(rel string) bool {
	return rel != "" && path.Clean(rel) == rel && !strings.Contains(rel, `\`) &&
		!strings.Contains(rel, ":") && filepath.IsLocal(filepath.FromSlash(rel))
}
