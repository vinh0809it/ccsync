// Package session reads Claude Code's local session storage
// (~/.claude/projects/<path-slug>/<session-id>.jsonl).
//
// The .jsonl transcript format is internal to Claude Code and changes
// between versions, so this package treats files as opaque blobs. It only
// (a) scans lines for a few metadata fields, degrading gracefully when
// they are missing, and (b) rewrites the exact `"cwd":"..."` substring
// when a session is installed under a different project path.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
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

// SlugForPath reproduces Claude Code's project-directory naming: every
// non-alphanumeric character of the absolute path becomes '-'.
// (Paths >200 chars get truncated+hashed by Claude Code; not handled here.)
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

// Session describes one local session transcript.
type Session struct {
	ID      string
	File    string // absolute path to the .jsonl
	Cwd     string // original project path, extracted from the transcript
	Title   string
	Version string // Claude Code version that wrote it
	Mtime   time.Time
	Size    int64
}

// validSlug matches Claude Code project-directory names (alphanumerics and
// dashes only) so an externally supplied slug can never traverse paths.
var validSlug = regexp.MustCompile(`^[A-Za-z0-9-]{1,255}$`)

// ListForProject returns the sessions stored for the project at absPath.
func ListForProject(absPath string) ([]Session, error) {
	return ListForSlug(SlugForPath(absPath))
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		file := filepath.Join(dir, e.Name())
		s, err := inspect(file)
		if err != nil {
			// Unreadable metadata must not hide the session.
			info, _ := e.Info()
			s = Session{ID: strings.TrimSuffix(e.Name(), ".jsonl"), File: file}
			if info != nil {
				s.Mtime = info.ModTime()
				s.Size = info.Size()
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
		if !e.IsDir() {
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
		if s, err := inspect(newest); err == nil {
			p.Path = s.Cwd
		}
		out = append(out, p)
	}
	return out, nil
}

// inspect scans a transcript for id, title, cwd and version without
// depending on the full line schema.
func inspect(file string) (Session, error) {
	s := Session{
		ID:   strings.TrimSuffix(filepath.Base(file), ".jsonl"),
		File: file,
	}
	info, err := os.Stat(file)
	if err != nil {
		return s, err
	}
	s.Mtime = info.ModTime()
	s.Size = info.Size()

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
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.Type == "custom-title" && s.Title == "" {
			s.Title = l.CustomTitle
		}
		if l.Cwd != "" && s.Cwd == "" {
			s.Cwd = l.Cwd
		}
		if l.Version != "" && s.Version == "" {
			s.Version = l.Version
		}
		if s.Title != "" && s.Cwd != "" && s.Version != "" {
			break
		}
	}
	return s, sc.Err()
}

// RewriteCwd swaps the structural cwd field from oldCwd to newCwd.
// Only the exact JSON substring `"cwd":"<oldCwd>"` is replaced; paths
// inside message content are deliberately left untouched.
func RewriteCwd(data []byte, oldCwd, newCwd string) []byte {
	if oldCwd == "" || oldCwd == newCwd {
		return data
	}
	oldJSON, err1 := json.Marshal(oldCwd)
	newJSON, err2 := json.Marshal(newCwd)
	if err1 != nil || err2 != nil {
		return data
	}
	return bytes.ReplaceAll(data,
		[]byte(`"cwd":`+string(oldJSON)),
		[]byte(`"cwd":`+string(newJSON)))
}

// validInstallID mirrors store.ValidSessionID: ids come from (possibly
// peer-written) store metadata and are used as a path component here.
var validInstallID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// Install writes transcript data as a session for the project at destPath,
// creating the project dir with the permissions Claude Code uses.
func Install(id string, data []byte, destPath string, force bool) (string, error) {
	if !validInstallID.MatchString(id) {
		return "", fmt.Errorf("refusing to install session with unsafe id %q", id)
	}
	claudeDir, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(claudeDir, "projects", SlugForPath(destPath))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, id+".jsonl")
	if !force {
		if _, err := os.Stat(target); err == nil {
			return "", fmt.Errorf("%s already exists locally (use --force to overwrite)", target)
		}
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return "", err
	}
	return target, nil
}
