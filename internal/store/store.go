// Package store manages the encrypted git-backed session store.
//
// Repo layout (everything under sessions/ is age-encrypted):
//
//	ccsync.json                     plaintext store marker {format: 1}
//	check.age                       encrypted known value, verifies passphrase
//	sessions/<session-id>/data.age  the transcript
//	sessions/<session-id>/meta.age  Meta (JSON), encrypted too — a public
//	                                repo leaks nothing but random UUIDs
//
// Git auth is delegated entirely to the system `git` (SSH keys or
// credential helpers the user already has). ccsync never sees or stores
// credentials.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ccsync/internal/crypto"
)

const markerFile = "ccsync.json"

// validID constrains session ids to safe path components. Anything a peer
// could smuggle into a store (a forged directory name or meta.session_id
// like "../../.bashrc") must never reach a filepath.Join.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// ValidSessionID reports whether id is safe to use as a path component.
func ValidSessionID(id string) bool { return validID.MatchString(id) }

// Meta is the per-session metadata blob (stored encrypted).
type Meta struct {
	SessionID     string    `json:"session_id"`
	Title         string    `json:"title,omitempty"`
	ProjectLabel  string    `json:"project_label"` // basename of original cwd
	OriginalCwd   string    `json:"original_cwd"`
	Hostname      string    `json:"hostname"`
	ClaudeVersion string    `json:"claude_version,omitempty"`
	SessionMtime  time.Time `json:"session_mtime"`
	Size          int64     `json:"size"`
	ContentHash   string    `json:"content_hash,omitempty"` // sha256 of the plaintext transcript
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

func git(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir,
		"-c", "user.name=ccsync", "-c", "user.email=ccsync@localhost"}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
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
		if _, err := git(".", "clone", repoURL, dir); err != nil {
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

	checkFile := filepath.Join(dir, "check.age")
	if blob, err := os.ReadFile(checkFile); err == nil {
		// Existing store: this machine just has to prove the passphrase.
		if err := crypto.VerifyCheckBlob(passphrase, blob); err != nil {
			return err
		}
	} else {
		// Brand-new store: create marker + passphrase check and push them.
		marker, _ := json.Marshal(map[string]int{"format": 1})
		if err := os.WriteFile(filepath.Join(dir, markerFile), append(marker, '\n'), 0o644); err != nil {
			return err
		}
		blob, err := crypto.NewCheckBlob(passphrase)
		if err != nil {
			return err
		}
		if err := os.WriteFile(checkFile, blob, 0o644); err != nil {
			return err
		}
		if _, err := git(dir, "add", "-A"); err != nil {
			return err
		}
		if _, err := git(dir, "commit", "-m", "ccsync: initialize store"); err != nil {
			return err
		}
		if _, err := git(dir, "push", "-u", "origin", "HEAD"); err != nil {
			return err
		}
	}
	return saveConfig(&Config{RepoURL: repoURL})
}

// Sync pulls the latest store state from the remote.
func Sync() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	_, err = git(dir, "pull", "--rebase", "--quiet")
	return err
}

// VerifyPassphrase checks the passphrase against the store's check blob.
func VerifyPassphrase(passphrase string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	blob, err := os.ReadFile(filepath.Join(dir, "check.age"))
	if err != nil {
		return fmt.Errorf("store has no check.age — run ccsync init first: %w", err)
	}
	return crypto.VerifyCheckBlob(passphrase, blob)
}

// Put encrypts and stages one session (transcript + meta) into the clone.
func Put(passphrase string, meta Meta, transcript []byte) error {
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
	dataBlob, err := crypto.Encrypt(passphrase, transcript)
	if err != nil {
		return err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	metaBlob, err := crypto.Encrypt(passphrase, metaJSON)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sdir, "data.age"), dataBlob, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sdir, "meta.age"), metaBlob, 0o644)
}

// Commit commits staged sessions and pushes to the remote. Only the
// sessions/ tree is committed — a stray plaintext file elsewhere in the
// clone must never end up in the (possibly public) remote.
func Commit(message string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if _, err := git(dir, "add", "-A", "--", "sessions"); err != nil {
		return err
	}
	out, err := git(dir, "status", "--porcelain", "--", "sessions")
	if err != nil {
		return err
	}
	if out == "" {
		return fmt.Errorf("nothing new to push")
	}
	if _, err := git(dir, "commit", "-m", message); err != nil {
		return err
	}
	_, err = git(dir, "push")
	return err
}

// List decrypts all session metadata in the store.
func List(passphrase string) ([]Meta, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dir, "sessions")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		// A store peer controls directory names and meta content; anything
		// that could poison a filepath.Join later is dropped here.
		if !e.IsDir() || !ValidSessionID(e.Name()) {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(root, e.Name(), "meta.age"))
		if err != nil {
			continue
		}
		plain, err := crypto.Decrypt(passphrase, blob)
		if err != nil {
			return nil, fmt.Errorf("session %s: %w", e.Name(), err)
		}
		var m Meta
		if err := json.Unmarshal(plain, &m); err != nil {
			continue
		}
		if m.SessionID != e.Name() {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// GetMeta returns the stored metadata for an exact session id, or found=false.
func GetMeta(passphrase, id string) (Meta, bool, error) {
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
	plain, err := crypto.Decrypt(passphrase, blob)
	if err != nil {
		return Meta{}, false, err
	}
	var m Meta
	if err := json.Unmarshal(plain, &m); err != nil {
		return Meta{}, false, err
	}
	if m.SessionID != id {
		return Meta{}, false, fmt.Errorf("session %s: metadata id mismatch", id)
	}
	return m, true, nil
}

// Get decrypts one session's transcript and metadata by (possibly
// abbreviated) session id.
func Get(passphrase, idPrefix string) (Meta, []byte, error) {
	metas, err := List(passphrase)
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
	plain, err := crypto.Decrypt(passphrase, blob)
	if err != nil {
		return Meta{}, nil, err
	}
	return *match, plain, nil
}
