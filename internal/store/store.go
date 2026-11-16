// Package store manages the encrypted git-backed session store.
//
// Repo layout (format 2):
//
//	ccsync.json                     plaintext marker {"format": 2}
//	check.age                       known value encrypted with the passphrase
//	key.age                         the store key, encrypted with the passphrase
//	sessions/<session-id>/data.age  the session bundle, encrypted to the key
//	                                (split into data.age.1, .2, ... past 45 MB)
//	sessions/<session-id>/meta.age  Meta (JSON), encrypted to the key — a
//	                                public repo leaks nothing but random UUIDs
//
// A Store is a local clone of that repo, used as a disposable cache of the
// remote: every operation starts by resetting it to the remote (Sync), so
// a failed push or an interrupted run can never leave it stuck or hide
// unpushed work. The source of truth is the remote plus each machine's
// Claude Code directory.
//
// Git auth is delegated entirely to the system `git` (SSH keys or
// credential helpers the user already has). ccsync never sees or stores
// credentials.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vinh-vh/ccsync/internal/crypto"
)

// Format is the store format this version reads and writes.
const Format = 2

const (
	markerFile  = "ccsync.json"
	checkFile   = "check.age" // kept in format 2 so ccsync 0.1.x never mistakes the store for an empty one
	keyFile     = "key.age"
	sessionsDir = "sessions"
)

var (
	// ErrNeedsMigration means the store still uses format 1.
	ErrNeedsMigration = errors.New("the store uses format 1 — upgrade ccsync on every machine, then run: ccsync migrate")
	// ErrNothingToCommit means no session changed.
	ErrNothingToCommit = errors.New("nothing new to push")
	// ErrRemoteMoved means another machine pushed first.
	ErrRemoteMoved = errors.New("the remote changed during the push")
)

// Meta is the per-session metadata blob (stored encrypted).
type Meta struct {
	SessionID     string    `json:"session_id"`
	Title         string    `json:"title,omitempty"`
	ProjectLabel  string    `json:"project_label"` // basename of original cwd
	OriginalCwd   string    `json:"original_cwd"`
	SessionDir    string    `json:"session_dir,omitempty"` // original companion directory
	Hostname      string    `json:"hostname"`
	ClaudeVersion string    `json:"claude_version,omitempty"`
	SessionMtime  time.Time `json:"session_mtime"`
	Size          int64     `json:"size"`
	ContentHash   string    `json:"content_hash,omitempty"` // sha256 of the uncompressed bundle
	PushedAt      time.Time `json:"pushed_at"`
}

// Store is the local clone of a store repo.
type Store struct {
	Dir string
	// Interactive lets git ask for credentials on the terminal. A GUI turns
	// it off: a prompt there would hang a request nobody can answer.
	Interactive bool
}

// Open returns the store whose clone is at dir.
func Open(dir string) *Store {
	return &Store{Dir: dir, Interactive: true}
}

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

// format returns the clone's store format (0 when there is no marker).
func (s *Store) format() (int, error) {
	data, err := os.ReadFile(s.path(markerFile))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var m struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return 0, fmt.Errorf("%s is corrupt: %w", markerFile, err)
	}
	return m.Format, nil
}

// Init clones (or adopts) the store repo, creates the store if the remote
// is empty, and verifies the passphrase.
func (s *Store) Init(repoURL, passphrase string) error {
	if _, err := os.Stat(s.path(".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(s.Dir), 0o700); err != nil {
			return err
		}
		if _, err := s.gitIn(".", "clone", "--quiet", repoURL, s.Dir); err != nil {
			return err
		}
	} else {
		// A clone already exists: it must track the same remote, or every
		// later push would silently land in the old repo while config
		// reports the new one.
		current, err := s.git("remote", "get-url", "origin")
		if err != nil {
			return err
		}
		if current != repoURL {
			return fmt.Errorf("store clone %s tracks %s, not %s — remove that directory (its content lives in the remote) and run init again", s.Dir, current, repoURL)
		}
	}
	if err := s.syncOrReset(); err != nil {
		return err
	}

	format, err := s.format()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(s.path(checkFile))
	switch {
	case format == 0 && os.IsNotExist(statErr):
		if err := s.create(passphrase); err != nil {
			return err
		}
	case format <= 1:
		// Format 1: verify only; Migrate upgrades it.
		if err := s.verifyCheck(passphrase); err != nil {
			return err
		}
	default:
		if _, err := s.Unlock(passphrase); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the local clone. The remote is untouched; this only
// forgets the store on this machine. It refuses a directory that isn't a
// ccsync clone, so a wrong path can't delete anything else.
func (s *Store) Delete() error {
	if _, err := os.Stat(s.Dir); os.IsNotExist(err) {
		return nil
	}
	_, gitErr := os.Stat(s.path(".git"))
	_, markerErr := os.Stat(s.path(markerFile))
	if gitErr != nil || (markerErr != nil && !isEmptyRepo(s)) {
		return fmt.Errorf("%s doesn't look like a ccsync store clone; not deleting it", s.Dir)
	}
	return os.RemoveAll(s.Dir)
}

// isEmptyRepo reports whether the clone has no commits (an init that never
// got its first push through).
func isEmptyRepo(s *Store) bool {
	_, err := s.git("rev-parse", "--verify", "--quiet", "HEAD")
	return err != nil
}

// syncOrReset brings a fresh or reused clone up to date for Init:
//   - a store upstream: Sync;
//   - another non-empty repo (e.g. one created with a README): fast-forward
//     to it, keeping its files, and build the store on top;
//   - no upstream: the remote must be empty, and whatever an earlier init
//     left locally (its first push may have been rejected) never reached
//     it, so it is discarded.
func (s *Store) syncOrReset() error {
	if _, err := s.git("rev-parse", "--verify", "--quiet", "@{u}"); err == nil {
		if _, err := s.git("fetch", "--quiet", "origin"); err != nil {
			return err
		}
		if s.upstreamIsStore() {
			return s.Sync()
		}
		_, err := s.git("merge", "--ff-only", "--quiet", "@{u}")
		return err
	}
	heads, err := s.git("ls-remote", "--heads", "origin")
	if err != nil {
		return err
	}
	if heads != "" {
		return fmt.Errorf("store clone %s is out of step with the remote — remove that directory and run init again", s.Dir)
	}
	_, _ = s.git("update-ref", "-d", "HEAD")
	for _, f := range []string{markerFile, checkFile, keyFile} {
		_ = os.Remove(s.path(f))
	}
	return nil
}

// create initializes a brand-new store in an empty clone and pushes it.
func (s *Store) create(passphrase string) error {
	key, err := crypto.NewKey()
	if err != nil {
		return err
	}
	if err := s.writeKeyFiles(passphrase, key); err != nil {
		return err
	}
	if _, err := s.git("add", "-A", "--", markerFile, checkFile, keyFile); err != nil {
		return err
	}
	if _, err := s.git("commit", "--quiet", "-m", "ccsync: initialize store"); err != nil {
		return err
	}
	_, err = s.git("push", "--quiet", "-u", "origin", "HEAD")
	return err
}

// writeKeyFiles writes the current-format marker, check file and wrapped key.
func (s *Store) writeKeyFiles(passphrase string, key *crypto.Key) error {
	marker, _ := json.Marshal(map[string]int{"format": Format})
	check, err := crypto.NewCheckBlob(passphrase)
	if err != nil {
		return err
	}
	wrapped, err := key.Wrap(passphrase)
	if err != nil {
		return err
	}
	return errors.Join(
		os.WriteFile(s.path(markerFile), append(marker, '\n'), 0o644),
		os.WriteFile(s.path(checkFile), check, 0o644),
		os.WriteFile(s.path(keyFile), wrapped, 0o644))
}

func (s *Store) verifyCheck(passphrase string) error {
	blob, err := os.ReadFile(s.path(checkFile))
	if err != nil {
		return err
	}
	return crypto.VerifyCheckBlob(passphrase, blob)
}

// Unlock verifies the passphrase and returns the store key. This is the
// one slow (scrypt) step of every operation.
func (s *Store) Unlock(passphrase string) (*crypto.Key, error) {
	format, err := s.format()
	if err != nil {
		return nil, err
	}
	switch {
	case format == 0:
		return nil, fmt.Errorf("the store is not initialized — run: ccsync init <git-repo-url>")
	case format == 1:
		return nil, ErrNeedsMigration
	case format > Format:
		return nil, fmt.Errorf("the store uses format %d — upgrade ccsync", format)
	}
	blob, err := os.ReadFile(s.path(keyFile))
	if err != nil {
		return nil, fmt.Errorf("store key missing: %w", err)
	}
	return crypto.UnwrapKey(passphrase, blob)
}
