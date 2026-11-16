package app

import (
	"fmt"
	"os"

	"github.com/vinh-vh/ccsync/internal/session"
	"github.com/vinh-vh/ccsync/internal/store"
)

// Stores manages the stores this machine is set up for.
type Stores struct {
	// Interactive lets git ask for credentials on the terminal; a GUI
	// turns it off.
	Interactive bool
}

func (m Stores) open(e store.Entry) *store.Store {
	st := store.Open(e.Dir)
	st.Interactive = m.Interactive
	return st
}

// List returns the registry.
func (m Stores) List() (*store.Registry, error) {
	return store.LoadRegistry()
}

// Add sets this machine up for a store — creating it when the repo is
// empty — makes it current and opens it. A clone created by a failed
// attempt (wrong passphrase, rejected push) is removed again.
func (m Stores) Add(name, repoURL, passphrase string) (*Client, error) {
	if err := store.CheckName(name); err != nil {
		return nil, err
	}
	reg, err := store.LoadRegistry()
	if err != nil {
		return nil, err
	}
	e, err := reg.Get(name)
	switch {
	case err == nil && e.RepoURL != repoURL:
		return nil, fmt.Errorf("a store named %q already uses %s — pick another name, or forget that store first", name, e.RepoURL)
	case err != nil:
		dir, err := store.DefaultDir(name)
		if err != nil {
			return nil, err
		}
		e = store.Entry{Name: name, RepoURL: repoURL, Dir: dir}
	}
	_, statErr := os.Stat(e.Dir)
	fresh := os.IsNotExist(statErr)
	st := m.open(e)
	if err := st.Init(repoURL, passphrase); err != nil {
		if fresh {
			_ = st.Delete()
		}
		return nil, err
	}
	reg.Put(e)
	if err := reg.Save(); err != nil {
		return nil, err
	}
	return unlock(e, st, passphrase)
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
	c, err := unlock(e, m.open(e), passphrase)
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

// Forget removes a store from this machine: its registry entry and local
// clone. The repo and every other machine are untouched, and local Claude
// Code sessions are not affected. This is the way out of a forgotten
// passphrase: forget the store, then set up a new one.
func (m Stores) Forget(name string) error {
	reg, err := store.LoadRegistry()
	if err != nil {
		return err
	}
	e, err := reg.Remove(name)
	if err != nil {
		return err
	}
	if err := m.open(e).Delete(); err != nil {
		return err
	}
	return reg.Save()
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
	return m.open(e).Migrate(passphrase, func(transcript []byte) ([]byte, string, error) {
		return session.Pack(session.Contents{Transcript: transcript})
	})
}
