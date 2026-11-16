package session

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// validSlug matches Claude Code project-directory names (alphanumerics and
// dashes only) so an externally supplied slug can never traverse paths.
var validSlug = regexp.MustCompile(`^[A-Za-z0-9-]{1,255}$`)

// projectDir returns Claude Code's storage directory for the project at
// absPath. Names over maxSlug characters get an unreproducible hash suffix,
// so for those an existing directory sharing the kept prefix is reused.
func projectDir(claudeDir, absPath string) (string, error) {
	slug := SlugForPath(absPath)
	root := filepath.Join(claudeDir, "projects")
	if len(slug) <= maxSlug {
		return filepath.Join(root, slug), nil
	}
	// Several long paths can share the kept prefix; the right folder is the
	// one whose sessions ran in absPath.
	entries, _ := os.ReadDir(root)
	var candidates []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), slug[:maxSlug]) {
			candidates = append(candidates, filepath.Join(root, e.Name()))
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	for _, dir := range candidates {
		if folderCwd(dir) == absPath {
			return dir, nil
		}
	}
	return "", fmt.Errorf("project path is too long for ccsync to derive Claude Code's folder name: start one Claude Code session in %s first, then retry", absPath)
}

// folderCwd returns the project path recorded by a session in dir.
func folderCwd(dir string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		if s, err := Load(f); err == nil && s.Cwd != "" {
			return s.Cwd
		}
	}
	return ""
}

// ResolveProject returns the absolute, symlink-free form of a project
// path — Claude Code files sessions under the real path. For a path that
// doesn't exist yet, its nearest existing ancestor is resolved.
func ResolveProject(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for dir := abs; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest), nil
		}
		if filepath.Dir(dir) == dir {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}
