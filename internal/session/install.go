package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Origin is where a session lived on the machine that pushed it.
type Origin struct {
	Cwd        string    // project path
	SessionDir string    // companion directory path
	Mtime      time.Time // last activity; kept so session lists sort as on the origin
}

var (
	// ErrLocalAhead means the local copy has entries the incoming one lacks.
	ErrLocalAhead = errors.New("the local copy is newer")
	// ErrDiverged means the local and incoming copies were both continued.
	ErrDiverged = errors.New("the local and incoming copies have diverged")
	// ErrUnknown means the copies can't be compared (no recognizable entries).
	ErrUnknown = errors.New("the local and incoming copies can't be compared")
)

// InstallResult reports what Install did.
type InstallResult struct {
	Target    string // installed transcript
	Unchanged bool   // the local transcript already matched
	Updated   bool   // an older local copy was fast-forwarded
	Backup    string // the local copy, when force replaced one that had entries of its own
}

// Install writes a session for the project at destPath, rewriting cwd
// fields and companion-directory references to the new location.
//
// An existing local transcript is replaced only when that loses nothing:
// when the incoming copy has all of its entries and more (see Compare). If
// the local copy is newer, has diverged, or can't be compared, Install
// returns ErrLocalAhead, ErrDiverged or ErrUnknown — unless force is set,
// in which case the local transcript and companion directory are kept next
// to them with a .bak-<unix time> suffix. Each file is written atomically,
// the transcript last.
func Install(id string, c *Contents, from Origin, destPath string, force bool) (InstallResult, error) {
	if !ValidID(id) {
		return InstallResult{}, fmt.Errorf("refusing to install session with unsafe id %q", id)
	}
	target, err := Target(id, destPath)
	if err != nil {
		return InstallResult{}, err
	}
	dir := filepath.Dir(target)
	res := InstallResult{Target: target}
	sessionDir := filepath.Join(dir, id)

	relocate := func(b []byte) []byte {
		return RewritePath(RewriteCwd(b, from.Cwd, destPath), from.SessionDir, sessionDir)
	}
	transcript := relocate(c.Transcript)

	existing, err := os.ReadFile(res.Target)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return res, err
	case bytes.Equal(existing, transcript):
		res.Unchanged = true
	default:
		switch rel := Compare(transcript, existing); {
		case rel == Same:
			res.Unchanged = true // differs only in paths or bookkeeping
		case rel == Ahead:
			res.Updated = true
		case !force && rel == Behind:
			return res, ErrLocalAhead
		case !force && rel == Unknown:
			return res, ErrUnknown
		case !force:
			return res, ErrDiverged
		default:
			// Forced over a copy with entries of its own: keep all of it.
			stamp := time.Now().Unix()
			res.Backup = fmt.Sprintf("%s.bak-%d", res.Target, stamp)
			if err := writeAtomic(res.Backup, existing); err != nil {
				return res, err
			}
			if _, err := os.Stat(sessionDir); err == nil {
				if err := os.Rename(sessionDir, fmt.Sprintf("%s.bak-%d", sessionDir, stamp)); err != nil {
					return res, err
				}
			}
		}
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
		if !from.Mtime.IsZero() {
			if err := os.Chtimes(res.Target, from.Mtime, from.Mtime); err != nil {
				return res, err
			}
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

// Target returns where Install puts session id for the project at destPath.
func Target(id, destPath string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("refusing to install session with unsafe id %q", id)
	}
	claudeDir, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	dir, err := projectDir(claudeDir, destPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".jsonl"), nil
}
