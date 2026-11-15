package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

// ListAll returns the sessions of every project.
func ListAll() ([]Session, error) {
	projects, err := ListProjects()
	if err != nil {
		return nil, err
	}
	var all []Session
	for _, p := range projects {
		sessions, err := ListForSlug(p.Slug)
		if err != nil {
			return nil, err
		}
		all = append(all, sessions...)
	}
	return all, nil
}
