package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vinh0809it/ccsync/internal/github"
	"github.com/vinh0809it/ccsync/internal/store"
)

// Stores manages the stores this machine is set up for.
func init() { store.TokenFor = github.Token }

type Stores struct {
	// Interactive lets git ask for credentials on the terminal; a GUI
	// turns it off.
	Interactive bool
}

func (m Stores) open(e store.Entry) *store.Store {
	st := store.Open(e)
	st.Interactive = m.Interactive
	return st
}

// List returns the registry.
func (m Stores) List() (*store.Registry, error) {
	return store.LoadRegistry()
}

// NewStore describes a store to set up.
type NewStore struct {
	Name string
	Kind store.Kind // git (default) or folder
	// Location is the repo URL (git) or the folder path (folder).
	Location string
	// CreateGitHubRepo, for a git store without a Location, creates this
	// private GitHub repo ("name" or "owner/name") through gh first.
	CreateGitHubRepo string
	// Account is the GitHub account git (and gh, to create the repo) uses
	// for this store; empty: git's own credentials and the active gh login.
	Account string
	// AllowPublic accepts a public GitHub repo. Sessions are encrypted
	// either way, but a public repo still shows when and how much you sync.
	AllowPublic bool
}

// ErrPublicRepo means the store repo is public and AllowPublic is unset.
var ErrPublicRepo = errors.New("this GitHub repo is public — use a private repo for your sessions (they are encrypted, but a public repo still shows when and how much you sync)")

// Add sets this machine up for a store — creating it when the repo or
// folder is empty — makes it current and opens it. A clone created by a
// failed attempt (wrong passphrase, rejected push) is removed again.
func (m Stores) Add(spec NewStore, passphrase string) (*Client, error) {
	if err := store.CheckName(spec.Name); err != nil {
		return nil, err
	}
	reg, err := store.LoadRegistry()
	if err != nil {
		return nil, err
	}
	old, oldErr := reg.Get(spec.Name)
	if oldErr == nil && spec.CreateGitHubRepo != "" {
		return nil, fmt.Errorf("a store named %q already exists — pick another name", spec.Name)
	}
	e, err := m.entry(spec)
	if err != nil {
		return nil, err
	}
	if oldErr == nil {
		if old.Kind != e.Kind || old.Location() != e.Location() {
			return nil, fmt.Errorf("a store named %q already uses %s — pick another name, or forget that store first", e.Name, old.Location())
		}
		e = old
	}

	st := m.open(e)
	unlock, err := st.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	_, statErr := os.Stat(e.Dir)
	fresh := os.IsNotExist(statErr)
	if err := st.Init(e.Location(), passphrase); err != nil {
		if fresh {
			_ = st.Forget()
		}
		return nil, err
	}
	reg.Put(e)
	if err := reg.Save(); err != nil {
		return nil, err
	}
	return unlockStore(e, st, passphrase)
}

// entry resolves a NewStore into a registry entry, creating the GitHub
// repo if asked and refusing public GitHub repos.
func (m Stores) entry(spec NewStore) (store.Entry, error) {
	e := store.Entry{Name: spec.Name, Kind: spec.Kind, Account: spec.Account}
	switch spec.Kind {
	case store.KindFolder:
		if spec.Location == "" {
			return e, fmt.Errorf("a folder store needs a folder")
		}
		dir, err := filepath.Abs(expandHome(spec.Location))
		if err != nil {
			return e, err
		}
		e.Dir = dir
		return e, nil
	case store.KindGit, "":
		e.Kind = store.KindGit
	default:
		return e, fmt.Errorf("unknown store kind %q", spec.Kind)
	}

	e.RepoURL = spec.Location
	if spec.CreateGitHubRepo != "" {
		if spec.Location != "" {
			return e, fmt.Errorf("give either a repo URL or a GitHub repo to create, not both")
		}
		url, err := github.CreatePrivate(spec.CreateGitHubRepo, spec.Account)
		if err != nil {
			return e, err
		}
		e.RepoURL = url
	}
	if e.RepoURL == "" {
		return e, fmt.Errorf("a git store needs a repo URL")
	}
	if github.Repo(e.RepoURL) != "" && !spec.AllowPublic {
		// Only a confirmed public repo is refused: without gh the
		// visibility can't be checked, and that must not block setup.
		if public, err := github.IsPublic(e.RepoURL, spec.Account); err == nil && public {
			return e, ErrPublicRepo
		}
	}
	dir, err := store.DefaultDir(spec.Name)
	if err != nil {
		return e, err
	}
	e.Dir = dir
	return e, nil
}

// SetAccount sets the GitHub account a store's git uses ("" for git's own
// credentials).
func (m Stores) SetAccount(name, account string) error {
	reg, err := store.LoadRegistry()
	if err != nil {
		return err
	}
	e, err := reg.Get(name)
	if err != nil {
		return err
	}
	if e.Kind == store.KindFolder && account != "" {
		return fmt.Errorf("%s is a synced folder: it uses no GitHub account", name)
	}
	if e.Account == account {
		return nil
	}
	e.Account = account
	reg.Put(e)
	return reg.Save()
}

// Unlock opens a store (the current one when name is empty) and makes it
// current.
func (m Stores) Unlock(name, passphrase string) (*Client, error) {
	reg, err := store.LoadRegistry()
	if err != nil {
		return nil, err
	}
	e, err := reg.Get(name)
	if err != nil {
		return nil, err
	}
	st := m.open(e)
	unlock, err := st.Lock()
	if err != nil {
		return nil, err
	}
	c, err := unlockStore(e, st, passphrase)
	unlock()
	if err != nil {
		return nil, err
	}
	if reg.Current != e.Name {
		reg.Current = e.Name
		if err := reg.Save(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Use makes a store current.
func (m Stores) Use(name string) error {
	reg, err := store.LoadRegistry()
	if err != nil {
		return err
	}
	if err := reg.Use(name); err != nil {
		return err
	}
	return reg.Save()
}

// Forget removes a store from this machine: its registry entry and, for a
// git store, the local clone. The repo or folder, every other machine, and
// local Claude Code sessions are untouched. This is the way out of a
// forgotten passphrase: forget the store, then set up a new one.
//
// The registry entry goes first: if the clone can't be removed, the store
// is still forgotten and the error says what is left behind.
func (m Stores) Forget(name string) error {
	reg, err := store.LoadRegistry()
	if err != nil {
		return err
	}
	e, err := reg.Remove(name)
	if err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	st := m.open(e)
	unlock, err := st.Lock()
	if err == nil {
		defer unlock()
		err = st.Forget()
	}
	if err != nil {
		return fmt.Errorf("forgot store %q, but its files at %s were left: %w", name, e.Dir, err)
	}
	return nil
}

// Migrate upgrades a store (the current one when name is empty) to the
// current format (see store.Migrate).
func (m Stores) Migrate(name, passphrase string) (int, error) {
	reg, err := store.LoadRegistry()
	if err != nil {
		return 0, err
	}
	e, err := reg.Get(name)
	if err != nil {
		return 0, err
	}
	st := m.open(e)
	unlock, err := st.Lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	return st.Migrate(passphrase)
}

// expandHome turns a leading "~" into the home directory, as a shell would
// (the GUI gets paths unexpanded).
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
