// Package app holds the operations shared by the CLI and the GUI, so both
// front-ends go through exactly the same code path.
package app

import (
	"errors"
	"fmt"

	"github.com/vinh-vh/ccsync/internal/crypto"
	"github.com/vinh-vh/ccsync/internal/session"
	"github.com/vinh-vh/ccsync/internal/store"
)

// publishAttempts bounds retries when other machines keep pushing first.
const publishAttempts = 3

// beforePush runs between commit and push; tests use it to simulate
// another machine pushing in that window.
var beforePush = func() {}

// Client runs operations against one unlocked store.
type Client struct {
	store *store.Store
	key   *crypto.Key
}

// Setup joins (or creates) the store at repoURL and opens it.
func Setup(st *store.Store, repoURL, passphrase string) (*Client, error) {
	if err := st.Init(repoURL, passphrase); err != nil {
		return nil, err
	}
	return Unlock(st, passphrase)
}

// Unlock syncs the store and opens it with the passphrase.
func Unlock(st *store.Store, passphrase string) (*Client, error) {
	if err := st.Sync(); err != nil {
		return nil, err
	}
	key, err := st.Unlock(passphrase)
	if err != nil {
		return nil, err
	}
	return &Client{store: st, key: key}, nil
}

// List syncs and returns the store's sessions, plus the ids of entries it
// can't read.
func (c *Client) List() (metas []store.Meta, unreadable []string, err error) {
	if err := c.store.Sync(); err != nil {
		return nil, nil, err
	}
	return c.store.List(c.key)
}

// publish stages changes on a freshly synced clone, commits and pushes
// them. When another machine pushes first, everything is redone on top of
// its push. stage returns how many sessions it changed; zero means there is
// nothing to publish.
func (c *Client) publish(message func(n int) string, stage func() (int, error)) error {
	for attempt := 1; ; attempt++ {
		if err := c.store.Sync(); err != nil {
			return err
		}
		n, err := stage()
		if err != nil || n == 0 {
			return err
		}
		if err := c.store.Commit(message(n)); err != nil {
			return err
		}
		beforePush()
		err = c.store.Push()
		// A failed push leaves an unpublished commit that the next Sync
		// discards, so a later run can never mistake it for published.
		if err == nil || !errors.Is(err, store.ErrRemoteMoved) || attempt == publishAttempts {
			return err
		}
	}
}

// Remove deletes sessions, given by (possibly abbreviated) ids, from the
// store. Local copies are untouched; the git history keeps old versions.
func (c *Client) Remove(idPrefixes []string) ([]store.Meta, error) {
	if err := c.store.Sync(); err != nil {
		return nil, err
	}
	var targets []store.Meta
	for _, p := range idPrefixes {
		m, err := c.store.Find(c.key, p)
		if err != nil {
			return nil, err
		}
		targets = append(targets, m)
	}
	err := c.publish(
		func(n int) string { return fmt.Sprintf("ccsync: remove %d session(s)", n) },
		func() (int, error) {
			n := 0
			for _, m := range targets {
				found, err := c.store.Remove(m.SessionID)
				if err != nil {
					return 0, err
				}
				if found {
					n++
				}
			}
			return n, nil
		})
	if err != nil {
		return nil, err
	}
	return targets, nil
}

// Migrate upgrades the store to the current format (see store.Migrate).
func Migrate(st *store.Store, passphrase string) (int, error) {
	return st.Migrate(passphrase, func(transcript []byte) ([]byte, string, error) {
		return session.Pack(session.Contents{Transcript: transcript})
	})
}
