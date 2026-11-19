// Package session reads and writes Claude Code's local session storage.
//
// A session is a transcript plus an optional companion directory:
//
//	~/.claude/projects/<slug>/<id>.jsonl      the transcript
//	~/.claude/projects/<slug>/<id>/           subagents/, tool-results/, ...
//
// The .jsonl format is internal to Claude Code and changes between
// versions, so this package treats files as opaque blobs. It only (a) scans
// lines for a few metadata fields, degrading gracefully when they are
// missing, and (b) rewrites two exact kinds of path when a session is
// installed somewhere else: the structural "cwd" fields and references to
// the session's own companion directory.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Session describes one local session.
type Session struct {
	ID      string
	File    string // absolute path to the .jsonl
	Dir     string // companion directory (may not exist)
	Cwd     string // original project path, extracted from the transcript
	Title   string
	Version string    // Claude Code version that wrote it
	Mtime   time.Time // newest modification of the transcript or companion files
	Size    int64     // transcript plus companion files
}

// validID constrains session ids to safe path components. Ids can come from
// peer-written store metadata and are used in filepath.Join.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// ValidID reports whether id is safe to use as a path component.
func ValidID(id string) bool { return validID.MatchString(id) }

// Load reads a session's metadata without depending on the full line
// schema. A big transcript is read only at both ends: the cwd and version
// are in its first lines, and the title is the last custom-title line,
// which Claude Code appends on a rename and re-appends as the session
// goes on, so the latest one sits near the end.
func Load(file string) (Session, error) {
	id := strings.TrimSuffix(filepath.Base(file), ".jsonl")
	s := Session{ID: id, File: file, Dir: filepath.Join(filepath.Dir(file), id)}
	info, err := os.Stat(file)
	if err != nil {
		return s, err
	}
	s.Mtime, s.Size = info.ModTime(), info.Size()
	_ = walkCompanion(s.Dir, func(_ string, fi os.FileInfo) error {
		s.Size += fi.Size()
		if fi.ModTime().After(s.Mtime) {
			s.Mtime = fi.ModTime()
		}
		return nil
	})

	f, err := os.Open(file)
	if err != nil {
		return s, err
	}
	defer f.Close()
	size := info.Size()
	if size <= headBytes+tailBytes {
		err := s.scanMeta(f)
		return s, err
	}
	if err := s.scanMeta(io.LimitReader(f, headBytes)); err != nil {
		return s, err
	}
	if _, err := f.Seek(size-tailBytes, io.SeekStart); err != nil {
		return s, err
	}
	// The window starts mid-line: skip to the next line.
	tail := bufio.NewReader(io.LimitReader(f, tailBytes))
	if _, err := tail.ReadBytes('\n'); err != nil {
		// One line fills the whole window: read everything.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return s, err
		}
		s = Session{ID: s.ID, File: s.File, Dir: s.Dir, Mtime: s.Mtime, Size: s.Size}
		err := s.scanMeta(f)
		return s, err
	}
	if err := s.scanMeta(tail); err != nil {
		return s, err
	}
	if s.Cwd == "" || s.Version == "" { // not in the first lines: look everywhere
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return s, err
		}
		title := s.Title
		err = s.scanMeta(f)
		if title != "" {
			s.Title = title // the tail's is the latest
		}
	}
	return s, err
}

// The ends of a transcript Load reads; a smaller one is read whole.
const (
	headBytes = 256 << 10
	tailBytes = 1 << 20
)

// scanMeta reads metadata fields from transcript lines: the first cwd and
// version, and the last title.
func (s *Session) scanMeta(r io.Reader) error {
	type line struct {
		Type        string `json:"type"`
		CustomTitle string `json:"customTitle"`
		Cwd         string `json:"cwd"`
		Version     string `json:"version"`
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		// Cheap pre-filter: most lines are large messages without the
		// fields still wanted.
		if !(bytes.Contains(b, []byte(`"custom-title"`)) ||
			(s.Cwd == "" && bytes.Contains(b, []byte(`"cwd"`))) ||
			(s.Version == "" && bytes.Contains(b, []byte(`"version"`)))) {
			continue
		}
		var l line
		if json.Unmarshal(b, &l) != nil {
			continue // also a line cut by the head window
		}
		if l.Type == "custom-title" && l.CustomTitle != "" {
			s.Title = l.CustomTitle
		}
		if s.Cwd == "" {
			s.Cwd = l.Cwd
		}
		if s.Version == "" {
			s.Version = l.Version
		}
	}
	return sc.Err()
}

// walkCompanion calls fn for every regular file under a companion
// directory with its slash-separated relative path. Symlinks and other
// special files are skipped; a missing directory is not an error.
func walkCompanion(dir string, fn func(rel string, fi os.FileInfo) error) error {
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), fi)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
