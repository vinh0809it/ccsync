// Package store manages the encrypted git-backed session store.
//
// Repo layout (format 2):
//
//	ccsync.json                     plaintext marker {"format": 2}
//	check.age                       known value encrypted with the passphrase
//	key.age                         the store key, encrypted with the passphrase
//	sessions/<session-id>/data.age  the session bundle, encrypted to the key
//	sessions/<session-id>/meta.age  Meta (JSON), encrypted to the key — a
//	                                public repo leaks nothing but random UUIDs
//
// The local clone is a disposable cache of the remote: every operation
// starts by resetting it to the remote, so a failed push or an interrupted
// run can never leave it stuck or hide unpushed work. The source of truth
// is the remote plus each machine's ~/.claude.
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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vinhvh0809/ccsync/internal/crypto"
	"github.com/vinhvh0809/ccsync/internal/session"
)

// Format is the store format this version reads and writes.
const Format = 2

const (
	markerFile = "ccsync.json"
	checkFile  = "check.age" // kept in format 2 so ccsync 0.1.x never mistakes the store for an empty one
	keyFile    = "key.age"
)

var (
	// ErrNeedsMigration means the store still uses format 1.
	ErrNeedsMigration = errors.New("the store uses format 1 — upgrade ccsync on every machine, then run: ccsync migrate")
	// ErrNothingToCommit means no session changed.
	ErrNothingToCommit = errors.New("nothing new to push")
	// ErrRemoteMoved means another machine pushed first.
	ErrRemoteMoved = errors.New("the remote changed during the push")
)

// ValidSessionID reports whether id is safe to use as a path component.
func ValidSessionID(id string) bool { return session.ValidID(id) }

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

// Config is the local (plaintext, credential-free) ccsync configuration.
type Config struct {
	RepoURL string `json:"repo_url"`
}

func configPath() (string, error) {
	if d := os.Getenv("CCSYNC_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "config.json"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ccsync", "config.json"), nil
}

// Dir returns the local clone of the store repo.
func Dir() (string, error) {
	if d := os.Getenv("CCSYNC_STORE_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "ccsync", "store"), nil
}

func LoadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("ccsync is not set up yet — run: ccsync init <git-repo-url>")
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveConfig(c *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, data, 0o600)
}

// NoPrompt stops git from asking for credentials on the terminal. The GUI
// sets it: a prompt there would hang the request with nobody to answer it.
var NoPrompt bool

func git(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir,
		"-c", "user.name=ccsync", "-c", "user.email=ccsync@localhost",
		// A global signing setup would prompt or fail on store commits.
		"-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	// English messages: Push recognizes a lost race by git's wording.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if NoPrompt {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// readFormat returns the store's format (0 when there is no marker).
func readFormat(dir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dir, markerFile))
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

// Init clones (or adopts) the store repo and sets up / verifies the passphrase.
func Init(repoURL, passphrase string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		if _, err := git(".", "clone", "--quiet", repoURL, dir); err != nil {
			return err
		}
	} else {
		// A clone already exists: it must track the same remote, or every
		// later push would silently land in the old repo while config
		// reports the new one.
		current, err := git(dir, "remote", "get-url", "origin")
		if err != nil {
			return err
		}
		if current != repoURL {
			return fmt.Errorf("store clone %s tracks %s, not %s — remove that directory (its content lives in the remote) and run init again", dir, current, repoURL)
		}
	}
	if _, err := git(dir, "rev-parse", "--verify", "--quiet", "@{u}"); err == nil {
		if err := Sync(); err != nil {
			return err
		}
	} else {
		// No upstream: the remote must be empty, and whatever an earlier
		// init left locally (its first push may have been rejected) never
		// reached it — start over.
		heads, err := git(dir, "ls-remote", "--heads", "origin")
		if err != nil {
			return err
		}
		if heads != "" {
			return fmt.Errorf("store clone %s is out of step with the remote — remove that directory and run init again", dir)
		}
		_, _ = git(dir, "update-ref", "-d", "HEAD")
		for _, f := range []string{markerFile, checkFile, keyFile} {
			_ = os.Remove(filepath.Join(dir, f))
		}
	}

	format, err := readFormat(dir)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(filepath.Join(dir, checkFile))
	switch {
	case format == 0 && os.IsNotExist(statErr):
		if err := create(dir, passphrase); err != nil {
			return err
		}
	case format <= 1:
		// Format 1: verify only; `ccsync migrate` upgrades it.
		blob, err := os.ReadFile(filepath.Join(dir, checkFile))
		if err != nil {
			return err
		}
		if err := crypto.VerifyCheckBlob(passphrase, blob); err != nil {
			return err
		}
	default:
		if _, err := Unlock(passphrase); err != nil {
			return err
		}
	}
	return saveConfig(&Config{RepoURL: repoURL})
}

// create initializes a brand-new store in an empty clone and pushes it.
func create(dir, passphrase string) error {
	key, err := crypto.NewKey()
	if err != nil {
		return err
	}
	if err := writeKeyFiles(dir, passphrase, key); err != nil {
		return err
	}
	if _, err := git(dir, "add", "-A", "--", markerFile, checkFile, keyFile); err != nil {
		return err
	}
	if _, err := git(dir, "commit", "--quiet", "-m", "ccsync: initialize store"); err != nil {
		return err
	}
	_, err = git(dir, "push", "--quiet", "-u", "origin", "HEAD")
	return err
}

// writeKeyFiles writes the format-2 marker, check file and wrapped key.
func writeKeyFiles(dir, passphrase string, key *crypto.Key) error {
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
		os.WriteFile(filepath.Join(dir, markerFile), append(marker, '\n'), 0o644),
		os.WriteFile(filepath.Join(dir, checkFile), check, 0o644),
		os.WriteFile(filepath.Join(dir, keyFile), wrapped, 0o644))
}

// Unlock verifies the passphrase and returns the store key. This is the
// one slow (scrypt) step of every operation.
func Unlock(passphrase string) (*crypto.Key, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	format, err := readFormat(dir)
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
	blob, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		return nil, fmt.Errorf("store key missing: %w", err)
	}
	return crypto.UnwrapKey(passphrase, blob)
}

// Sync makes the local clone an exact copy of the remote, discarding
// anything a failed or interrupted run left behind.
func Sync() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(dir, ".git", d)); err == nil {
			_, _ = git(dir, "rebase", "--abort")
		}
	}
	if _, err := git(dir, "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	// Sync discards local changes: be sure this is a ccsync store first.
	if _, err := git(dir, "cat-file", "-e", "@{u}:"+markerFile); err != nil {
		return fmt.Errorf("%s does not track a ccsync store (no %s upstream) — check CCSYNC_STORE_DIR or run ccsync init", dir, markerFile)
	}
	if _, err := git(dir, "reset", "--hard", "--quiet", "@{u}"); err != nil {
		return err
	}
	_, err = git(dir, "clean", "-fdq")
	return err
}

// Put encrypts and stages one session into the clone.
func Put(key *crypto.Key, meta Meta, payload []byte) error {
	if !ValidSessionID(meta.SessionID) {
		return fmt.Errorf("invalid session id %q", meta.SessionID)
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	sdir := filepath.Join(dir, "sessions", meta.SessionID)
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		return err
	}
	dataBlob, err := key.Encrypt(payload)
	if err != nil {
		return err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	metaBlob, err := key.Encrypt(metaJSON)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sdir, "data.age"), dataBlob, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sdir, "meta.age"), metaBlob, 0o644)
}

// Commit commits staged changes. Only the sessions/ tree and the key files
// are committed — a stray plaintext file elsewhere in the clone must never
// end up in the (possibly public) remote.
func Commit(message string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	paths := []string{"sessions", markerFile, checkFile, keyFile}
	if _, err := git(dir, append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return err
	}
	out, err := git(dir, append([]string{"status", "--porcelain", "--"}, paths...)...)
	if err != nil {
		return err
	}
	if out == "" {
		return ErrNothingToCommit
	}
	_, err = git(dir, "commit", "--quiet", "-m", message)
	return err
}

// Push publishes local commits. It returns ErrRemoteMoved (wrapped) when
// another machine pushed first; the caller re-syncs and retries.
func Push() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	_, err = git(dir, "push", "--quiet")
	if err != nil && (strings.Contains(err.Error(), "fetch first") ||
		strings.Contains(err.Error(), "non-fast-forward")) {
		return fmt.Errorf("%w: %v", ErrRemoteMoved, err)
	}
	return err
}

// List decrypts all session metadata in the store. Entries that can't be
// read (e.g. pushed by an outdated ccsync) are reported in unreadable
// instead of hiding the rest.
func List(key *crypto.Key) (metas []Meta, unreadable []string, err error) {
	dir, err := Dir()
	if err != nil {
		return nil, nil, err
	}
	root := filepath.Join(dir, "sessions")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		// A store peer controls directory names and meta content; anything
		// that could poison a filepath.Join later is dropped here.
		if !e.IsDir() || !ValidSessionID(e.Name()) {
			continue
		}
		m, found, err := GetMeta(key, e.Name())
		if err != nil {
			unreadable = append(unreadable, e.Name())
			continue
		}
		if found {
			metas = append(metas, m)
		}
	}
	return metas, unreadable, nil
}

// GetMeta returns the stored metadata for an exact session id, or found=false.
func GetMeta(key *crypto.Key, id string) (Meta, bool, error) {
	if !ValidSessionID(id) {
		return Meta{}, false, fmt.Errorf("invalid session id %q", id)
	}
	dir, err := Dir()
	if err != nil {
		return Meta{}, false, err
	}
	blob, err := os.ReadFile(filepath.Join(dir, "sessions", id, "meta.age"))
	if os.IsNotExist(err) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, false, err
	}
	plain, err := key.Decrypt(blob)
	if err != nil {
		return Meta{}, false, fmt.Errorf("session %s: %w", id, err)
	}
	var m Meta
	if err := json.Unmarshal(plain, &m); err != nil {
		return Meta{}, false, fmt.Errorf("session %s: %w", id, err)
	}
	if m.SessionID != id {
		return Meta{}, false, fmt.Errorf("session %s: metadata id mismatch", id)
	}
	return m, true, nil
}

// Get decrypts one session's bundle and metadata by (possibly abbreviated)
// session id.
func Get(key *crypto.Key, idPrefix string) (Meta, []byte, error) {
	if idPrefix == "" {
		return Meta{}, nil, fmt.Errorf("empty session id")
	}
	metas, _, err := List(key)
	if err != nil {
		return Meta{}, nil, err
	}
	var match *Meta
	for i := range metas {
		if strings.HasPrefix(metas[i].SessionID, idPrefix) {
			if match != nil {
				return Meta{}, nil, fmt.Errorf("session id %q is ambiguous", idPrefix)
			}
			match = &metas[i]
		}
	}
	if match == nil {
		return Meta{}, nil, fmt.Errorf("no session in the store matches %q", idPrefix)
	}
	dir, err := Dir()
	if err != nil {
		return Meta{}, nil, err
	}
	blob, err := os.ReadFile(filepath.Join(dir, "sessions", match.SessionID, "data.age"))
	if err != nil {
		return Meta{}, nil, err
	}
	plain, err := key.Decrypt(blob)
	if err != nil {
		return Meta{}, nil, err
	}
	return *match, plain, nil
}

// Migrate upgrades a format-1 store to format 2 and converts any session
// still encrypted the format-1 way (one pushed by an outdated ccsync after
// an earlier migration). convert turns a raw format-1 transcript into a
// bundle and its hash. It returns how many sessions were converted.
func Migrate(passphrase string, convert func(transcript []byte) ([]byte, string, error)) (int, error) {
	if err := Sync(); err != nil {
		return 0, err
	}
	dir, err := Dir()
	if err != nil {
		return 0, err
	}
	format, err := readFormat(dir)
	if err != nil {
		return 0, err
	}
	var key *crypto.Key
	switch format {
	case 1:
		blob, err := os.ReadFile(filepath.Join(dir, checkFile))
		if err != nil {
			return 0, err
		}
		if err := crypto.VerifyCheckBlob(passphrase, blob); err != nil {
			return 0, err
		}
		if key, err = crypto.NewKey(); err != nil {
			return 0, err
		}
		if err := writeKeyFiles(dir, passphrase, key); err != nil {
			return 0, err
		}
	case Format:
		if key, err = Unlock(passphrase); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("can't migrate a store with format %d", format)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	converted := 0
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !ValidSessionID(id) {
			continue
		}
		if _, _, err := GetMeta(key, id); err == nil {
			continue // already format 2
		}
		sdir := filepath.Join(dir, "sessions", id)
		var m Meta
		metaBlob, err1 := os.ReadFile(filepath.Join(sdir, "meta.age"))
		dataBlob, err2 := os.ReadFile(filepath.Join(sdir, "data.age"))
		if err := errors.Join(err1, err2); err != nil {
			return 0, fmt.Errorf("session %s: %w", id, err)
		}
		metaJSON, err := crypto.Decrypt(passphrase, metaBlob)
		if err != nil {
			return 0, fmt.Errorf("session %s: %w", id, err)
		}
		if err := json.Unmarshal(metaJSON, &m); err != nil || m.SessionID != id {
			return 0, fmt.Errorf("session %s: unreadable metadata", id)
		}
		transcript, err := crypto.Decrypt(passphrase, dataBlob)
		if err != nil {
			return 0, fmt.Errorf("session %s: %w", id, err)
		}
		payload, hash, err := convert(transcript)
		if err != nil {
			return 0, fmt.Errorf("session %s: %w", id, err)
		}
		m.ContentHash = hash
		if err := Put(key, m, payload); err != nil {
			return 0, err
		}
		converted++
	}
	if err := Commit(fmt.Sprintf("ccsync: migrate store to format %d", Format)); err != nil {
		if errors.Is(err, ErrNothingToCommit) {
			return 0, nil
		}
		return 0, err
	}
	return converted, Push()
}
