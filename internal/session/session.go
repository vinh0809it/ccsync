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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ClaudeDir returns the Claude Code config dir ($CLAUDE_CONFIG_DIR or ~/.claude).
func ClaudeDir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// maxSlug is the longest project-directory name Claude Code uses as is;
// longer ones are cut and given a hash suffix that can't be reproduced.
const maxSlug = 200

// SlugForPath reproduces Claude Code's project-directory naming: every
// non-alphanumeric character of the absolute path becomes '-'.
func SlugForPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

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

// validSlug matches Claude Code project-directory names (alphanumerics and
// dashes only) so an externally supplied slug can never traverse paths.
var validSlug = regexp.MustCompile(`^[A-Za-z0-9-]{1,255}$`)

// validID constrains session ids to safe path components. Ids can come from
// peer-written store metadata and are used in filepath.Join.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// ValidID reports whether id is safe to use as a path component.
func ValidID(id string) bool { return validID.MatchString(id) }

// ListForProject returns the sessions stored for the project at absPath.
func ListForProject(absPath string) ([]Session, error) {
	claudeDir, err := ClaudeDir()
	if err != nil {
		return nil, err
	}
	dir, err := projectDir(claudeDir, absPath)
	if err != nil {
		return nil, err
	}
	return ListForSlug(filepath.Base(dir))
}

// ListForSlug returns the sessions in one project storage directory,
// addressed by its on-disk name. This is exact — unlike deriving the slug
// from a path, it also works for slugs Claude Code truncates and hashes.
func ListForSlug(slug string) ([]Session, error) {
	if !validSlug.MatchString(slug) {
		return nil, fmt.Errorf("invalid project slug %q", slug)
	}
	claudeDir, err := ClaudeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(claudeDir, "projects", slug)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("no sessions found in %s", dir)
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		id, isTranscript := strings.CutSuffix(e.Name(), ".jsonl")
		if e.IsDir() || !isTranscript || !ValidID(id) {
			continue
		}
		file := filepath.Join(dir, e.Name())
		s, err := Load(file)
		if err != nil {
			// Unreadable metadata must not hide the session.
			s = Session{ID: id, File: file, Dir: filepath.Join(dir, id)}
			if info, err := e.Info(); err == nil {
				s.Mtime, s.Size = info.ModTime(), info.Size()
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// Project is one project directory found in Claude Code's storage.
type Project struct {
	Path     string    // real project path, recovered from a transcript's cwd
	Slug     string    // storage directory name
	Sessions int       // number of .jsonl transcripts
	LastUsed time.Time // newest transcript mtime
}

// ListProjects scans ~/.claude/projects and recovers each project's real
// path from its newest transcript (the slug alone is lossy). Projects whose
// transcripts carry no cwd are reported with an empty Path.
func ListProjects() ([]Project, error) {
	claudeDir, err := ClaudeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(claudeDir, "projects")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Project
	for _, e := range entries {
		if !e.IsDir() || !validSlug.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		p := Project{Slug: e.Name()}
		var newest string
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			p.Sessions++
			if info, err := f.Info(); err == nil && info.ModTime().After(p.LastUsed) {
				p.LastUsed = info.ModTime()
				newest = filepath.Join(dir, f.Name())
			}
		}
		if p.Sessions == 0 {
			continue
		}
		if s, err := Load(newest); err == nil {
			p.Path = s.Cwd
		}
		out = append(out, p)
	}
	return out, nil
}

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

// jsonString is s as a JSON string body (no quotes), escaped the way
// Claude Code's JSON.stringify writes it.
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSpace(buf.String())
	return out[1 : len(out)-1]
}

// RewriteCwd moves the structural cwd fields from oldCwd to newCwd. Both
// the project root and its subdirectories ("cwd":"<old>/sub") are
// rewritten; paths inside message content are deliberately left alone.
func RewriteCwd(data []byte, oldCwd, newCwd string) []byte {
	if oldCwd == "" || oldCwd == newCwd {
		return data
	}
	oldP := `"cwd":"` + jsonString(oldCwd)
	newP := `"cwd":"` + jsonString(newCwd)
	for _, end := range []string{`"`, `/`, `\\`} {
		data = bytes.ReplaceAll(data, []byte(oldP+end), []byte(newP+end))
	}
	return data
}

// RewritePath replaces every occurrence of the JSON-encoded oldPath with
// newPath. Used for the session's companion directory, whose path (ending
// in the session uuid) is specific enough to rewrite anywhere — Claude
// Code points at it from message content ("Full output saved to: ...").
func RewritePath(data []byte, oldPath, newPath string) []byte {
	if oldPath == "" || oldPath == newPath {
		return data
	}
	return bytes.ReplaceAll(data, []byte(jsonString(oldPath)), []byte(jsonString(newPath)))
}

// projectDir returns Claude Code's storage directory for the project at
// absPath. Names over maxSlug characters get an unreproducible hash suffix,
// so for those an existing directory sharing the kept prefix is reused.
func projectDir(claudeDir, absPath string) (string, error) {
	slug := SlugForPath(absPath)
	root := filepath.Join(claudeDir, "projects")
	if len(slug) <= maxSlug {
		return filepath.Join(root, slug), nil
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), slug[:maxSlug]) {
			return filepath.Join(root, e.Name()), nil
		}
	}
	return "", fmt.Errorf("project path is too long for ccsync to derive Claude Code's folder name: start one Claude Code session in %s first, then retry", absPath)
}

// ResolveProject returns the absolute, symlink-free form of a project
// path — Claude Code files sessions under the real path.
func ResolveProject(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return abs, nil
}
