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
// schema. The whole transcript is scanned: a rename appends a new
// custom-title line, so the last one wins.
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

	type line struct {
		Type        string `json:"type"`
		CustomTitle string `json:"customTitle"`
		Cwd         string `json:"cwd"`
		Version     string `json:"version"`
	}
	sc := bufio.NewScanner(f)
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
			continue
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
	return s, sc.Err()
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
