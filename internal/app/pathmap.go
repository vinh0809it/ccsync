package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PathRule moves projects under From to the same place under To.
type PathRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PathMap says where projects of another machine live on this one, e.g.
// /home/alice/dev → /Users/alice/code. Paths no rule covers are kept.
type PathMap []PathRule

// ParseRule parses "FROM=TO".
func ParseRule(s string) (PathRule, error) {
	from, to, ok := strings.Cut(s, "=")
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if !ok || from == "" || to == "" {
		return PathRule{}, fmt.Errorf("invalid path mapping %q: want FROM=TO", s)
	}
	return PathRule{From: from, To: to}, nil
}

// Apply returns where the project at p (a path from another machine,
// possibly another OS) lives here. The longest matching From wins; a rule
// matches only whole path components, so /home/a doesn't cover /home/ab.
func (m PathMap) Apply(p string) string {
	best := -1
	for i, r := range m {
		if covers(r.From, p) && (best < 0 || len(r.From) > len(m[best].From)) {
			best = i
		}
	}
	if best < 0 {
		return p
	}
	rest := strings.Trim(strings.ReplaceAll(p[len(trimSep(m[best].From)):], `\`, "/"), "/")
	to := expandHome(m[best].To)
	if rest == "" {
		return filepath.Clean(to)
	}
	return filepath.Join(to, filepath.FromSlash(rest))
}

func trimSep(p string) string {
	if t := strings.TrimRight(p, `/\`); t != "" {
		return t
	}
	return p
}

// covers reports whether path p is from or inside it, either OS's style.
func covers(from, p string) bool {
	from = trimSep(from)
	if !strings.HasPrefix(p, from) {
		return false
	}
	rest := p[len(from):]
	return rest == "" || rest[0] == '/' || rest[0] == '\\'
}

// homeOf matches the home directory at the start of a Linux, macOS or
// Windows path.
var homeOf = regexp.MustCompile(`^(/home/[^/]+|/Users/[^/]+|[A-Za-z]:\\Users\\[^\\]+)(?:[/\\]|$)`)

// SuggestPathMap proposes rules for projects from other machines: when
// their paths are under another home directory than this machine's (say
// /home/alice there, /Users/alice here), map that home to this one. Homes
// that already exist here are left alone.
func SuggestPathMap(cwds []string) PathMap {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var m PathMap
	for _, cwd := range cwds {
		sub := homeOf.FindStringSubmatch(cwd)
		if sub == nil || seen[sub[1]] {
			continue
		}
		seen[sub[1]] = true
		if sub[1] == home {
			continue
		}
		if _, err := os.Stat(sub[1]); err == nil {
			continue // that home exists here: keep the paths
		}
		m = append(m, PathRule{From: sub[1], To: home})
	}
	sort.Slice(m, func(i, j int) bool { return m[i].From < m[j].From })
	return m
}
