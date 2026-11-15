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

// Contents is an unpacked bundle.
type Contents struct {
	Transcript []byte
	Companion  map[string][]byte // slash-separated path relative to the companion dir
}

// Bundle packs a local session. The hash covers the uncompressed tar,
// which is deterministic (sorted names, fixed modes and times), so an
// unchanged session always hashes the same.
func Bundle(s Session) (payload []byte, hash string, err error) {
	c := Contents{Companion: map[string][]byte{}}
	if c.Transcript, err = os.ReadFile(s.File); err != nil {
		return nil, "", err
	}
	err = walkCompanion(s.Dir, func(rel string, _ os.FileInfo) error {
		data, err := os.ReadFile(filepath.Join(s.Dir, filepath.FromSlash(rel)))
		c.Companion[rel] = data
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return Pack(c)
}

// Pack builds a bundle from contents.
func Pack(c Contents) (payload []byte, hash string, err error) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
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
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(tarBuf.Bytes())

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(tarBuf.Bytes()); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return gz.Bytes(), hex.EncodeToString(sum[:]), nil
}

// Unpack validates and unpacks a bundle. Bundles come from the store, which
// other machines write, so every name is checked before it can reach a
// filesystem path.
func Unpack(payload []byte) (*Contents, error) {
	zr, err := gzip.NewReader(bytes.NewReader(payload))
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
		data, err := io.ReadAll(tr)
		if err != nil {
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

// Origin is where a session lived on the machine that pushed it.
type Origin struct {
	Cwd        string // project path
	SessionDir string // companion directory path
}

// ErrLocalDiffers means a different local copy of the session exists.
var ErrLocalDiffers = errors.New("a different local copy of this session exists")

// InstallResult reports what Install did.
type InstallResult struct {
	Target    string // installed transcript
	Backup    string // previous local transcript, when one was replaced
	Unchanged bool   // the local transcript already matched
}

// Install writes a session for the project at destPath, rewriting cwd
// fields and companion-directory references to the new location. An
// existing local transcript that differs is only replaced when force is
// set, and is then kept next to it as <id>.jsonl.bak-<unix time>. Each file
// is written atomically, the transcript last.
func Install(id string, c *Contents, from Origin, destPath string, force bool) (InstallResult, error) {
	if !ValidID(id) {
		return InstallResult{}, fmt.Errorf("refusing to install session with unsafe id %q", id)
	}
	claudeDir, err := ClaudeDir()
	if err != nil {
		return InstallResult{}, err
	}
	dir, err := projectDir(claudeDir, destPath)
	if err != nil {
		return InstallResult{}, err
	}
	res := InstallResult{Target: filepath.Join(dir, id+".jsonl")}
	sessionDir := filepath.Join(dir, id)

	relocate := func(b []byte) []byte {
		return RewritePath(RewriteCwd(b, from.Cwd, destPath), from.SessionDir, sessionDir)
	}
	transcript := relocate(c.Transcript)

	existing, err := os.ReadFile(res.Target)
	switch {
	case err == nil && bytes.Equal(existing, transcript):
		res.Unchanged = true
	case err == nil && !force:
		return res, ErrLocalDiffers
	case err == nil:
		res.Backup = fmt.Sprintf("%s.bak-%d", res.Target, time.Now().Unix())
		if err := writeAtomic(res.Backup, existing); err != nil {
			return res, err
		}
	case !os.IsNotExist(err):
		return res, err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	for rel, data := range c.Companion {
		if strings.HasSuffix(rel, ".jsonl") {
			data = relocate(data)
		}
		p := filepath.Join(sessionDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return res, err
		}
		if err := writeAtomic(p, data); err != nil {
			return res, err
		}
	}
	if !res.Unchanged {
		if err := writeAtomic(res.Target, transcript); err != nil {
			return res, err
		}
	}
	return res, nil
}

// writeAtomic replaces path with data via a temp file and rename, so a
// crash never leaves a half-written file behind.
func writeAtomic(p string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".ccsync-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Sync(), f.Close(), os.Chmod(tmp, 0o600)); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
