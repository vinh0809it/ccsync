// Package store manages an encrypted session store.
//
// Store layout (format 2), the same for every kind of store:
//
//	ccsync.json                     plaintext marker {"format": 2}
//	check.age                       known value encrypted with the passphrase
//	key.age                         the store key, encrypted with the passphrase
//	sessions/<session-id>/data.age  the session bundle, encrypted to the key
//	                                (split into data.age.1, .2, ... past 45 MB)
//	sessions/<session-id>/meta.age  Meta (JSON), encrypted to the key — a
//	                                reader of the files learns nothing but
//	                                random UUIDs
//
// Where the files live depends on the store's kind (see transport):
//
//   - git: a local clone of a repo, used as a disposable cache of the
//     remote. Every operation starts by resetting it to the remote, so a
//     failed push or an interrupted run can never leave it stuck or hide
//     unpushed work. Auth is the system git's (SSH keys, or credential
//     helpers such as `gh auth setup-git`); ccsync never sees credentials.
//   - folder: a directory that a sync service (Dropbox, iCloud Drive,
//     Google Drive, OneDrive, Syncthing, a network share, ...) keeps in
//     step across machines. Files are written atomically and read in place.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vinh0809it/ccsync/internal/crypto"
	"github.com/vinh0809it/ccsync/internal/session"
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
	// ErrNoHistory means the store keeps no history to compact.
	ErrNoHistory = errors.New("a synced-folder store keeps no history — there is nothing to compact")
)

// CompactResult is what Compact did.
type CompactResult struct {
	Commits int   `json:"commits"` // in the history before; 1 means it was already compact
	Before  int64 `json:"before"`  // bytes the local clone's history took
	After   int64 `json:"after"`
}

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
	// Entries fingerprints the transcript's entries (session.Fingerprint),
	// so a copy elsewhere can be found in sync without decrypting the
	// bundle. Empty for copies pushed before ccsync 0.2.0.
	Entries  string    `json:"entries,omitempty"`
	PushedAt time.Time `json:"pushed_at"`
	// Preview holds the session's last messages, so they show without
	// downloading the bundle. Empty for copies pushed before it existed.
	Preview []session.Message `json:"preview,omitempty"`
}

// Store is one store as this machine sees it.
type Store struct {
	Dir string // the store's files: the git clone, or the synced folder
	// Interactive lets git ask for credentials on the terminal. A GUI turns
	// it off: a prompt there would hang a request nobody can answer.
	Interactive bool

	lockFile string // serializes ccsync processes using this store
	t        transport
}

// Open returns the store described by e.
func Open(e Entry) *Store {
	s := &Store{Dir: e.Dir, Interactive: true}
	if e.Name != "" {
		s.lockFile, _ = lockPath(e.Name)
	}
	if e.Kind == KindFolder {
		s.t = folderTransport{s}
	} else {
		s.t = gitTransport{s}
	}
	return s
}

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

// format returns the store's format (0 when there is no marker).
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

// Init connects to the store at location (a repo URL, or the folder path
// for a folder store), creates the store if there is none yet, and
// verifies the passphrase.
func (s *Store) Init(location, passphrase string) error {
	if err := s.t.open(location); err != nil {
		return err
	}
	format, err := s.format()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(s.path(checkFile))
	switch {
	case format == 0 && os.IsNotExist(statErr):
		return s.create(passphrase)
	case format <= 1:
		// Format 1: verify only; Migrate upgrades it.
		return s.verifyCheck(passphrase)
	default:
		_, err := s.Unlock(passphrase)
		return err
	}
}

// create initializes a brand-new store and publishes it.
func (s *Store) create(passphrase string) error {
	key, err := crypto.NewKey()
	if err != nil {
		return err
	}
	if err := s.writeKeyFiles(passphrase, key); err != nil {
		return err
	}
	if err := s.t.commit("ccsync: initialize store"); err != nil {
		return err
	}
	return s.t.push(nil)
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
		writeAtomic(s.path(markerFile), append(marker, '\n')),
		writeAtomic(s.path(checkFile), check),
		writeAtomic(s.path(keyFile), wrapped))
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
		return nil, fmt.Errorf("no ccsync store found in %s — set the store up again", s.Dir)
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

// Sync brings the store up to date with other machines (see transport).
func (s *Store) Sync() error { return s.t.sync() }

// Commit records staged changes; ErrNothingToCommit when there are none.
func (s *Store) Commit(message string) error { return s.t.commit(message) }

// Push publishes committed changes; ErrRemoteMoved (wrapped) when another
// machine pushed first, in which case the caller re-syncs and retries.
func (s *Store) Push() error { return s.t.push(nil) }

// PushProgress is Push, telling progress how the upload advances.
func (s *Store) PushProgress(progress func(UploadProgress)) error { return s.t.push(progress) }

// History is the size of a git store's history (see Store.History).
type History struct {
	Commits int   `json:"commits"`
	Bytes   int64 `json:"bytes"` // this machine's clone (about the remote's); -1: unknown, a partial clone
}

// History reports how much the store's history holds, to tell when
// Compact is worth it. ErrNoHistory for a synced folder.
func (s *Store) History() (History, error) { return s.t.history() }

// Compact replaces the store's history with a single commit holding its
// current files, so replaced and deleted versions stop taking space. Old
// versions can't be recovered afterwards. ErrRemoteMoved (wrapped) when
// another machine pushed meanwhile; ErrNoHistory for a synced folder.
func (s *Store) Compact() (CompactResult, error) { return s.t.compact() }

// Forget removes what this machine keeps of the store — for a git store
// its clone — never the shared copy itself.
func (s *Store) Forget() error { return s.t.forget() }

// writeAtomic replaces path with data via a temp file and rename, so a
// crash never leaves a half-written file and a sync service never ships
// one to other machines.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Close()); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// tempPrefix marks files being written; readers and sync ignore them.
const tempPrefix = ".ccsync-tmp-"
