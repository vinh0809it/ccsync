// Package app holds the operations shared by the CLI and the GUI, so both
// front-ends go through exactly the same code path.
package app

import (
	"errors"
	"fmt"

	"github.com/vinh0809it/ccsync/internal/crypto"
	"github.com/vinh0809it/ccsync/internal/store"
)

// publishAttempts bounds retries when other machines keep pushing first.
const publishAttempts = 3

// beforePush runs between commit and push; tests use it to simulate
// another machine pushing in that window.
var beforePush = func() {}

// Client runs operations against one unlocked store.
type Client struct {
	Entry store.Entry // which store
	store *store.Store
	key   *crypto.Key
}

// unlockStore syncs the store and opens it with the passphrase. The caller
// holds the store's lock.
func unlockStore(e store.Entry, st *store.Store, passphrase string) (*Client, error) {
	if err := st.Sync(); err != nil {
		return nil, err
	}
	key, err := st.Unlock(passphrase)
	if err != nil {
		return nil, err
	}
	return &Client{Entry: e, store: st, key: key}, nil
}

// locked runs f under the store's lock: one ccsync process at a time (the
// GUI and a CLI command, say) syncs, writes and publishes a store.
func (c *Client) locked(f func() error) error {
	unlock, err := c.store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return f()
}

// List syncs and returns the store's sessions, plus the ids of entries it
// can't read.
func (c *Client) List() (metas []store.Meta, unreadable []string, err error) {
	err = c.locked(func() error {
		if err := c.store.Sync(); err != nil {
			return err
		}
		metas, unreadable, err = c.store.List(c.key)
		return err
	})
	return metas, unreadable, err
}

// publish stages changes on a freshly synced store, commits and pushes
// them. When another machine pushes first, everything is redone on top of
// its push. stage returns how many sessions it changed; zero means there is
// nothing to publish. The caller holds the lock.
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
// store. Local copies are untouched; a git store's history keeps old
// versions.
func (c *Client) Remove(idPrefixes []string) (removed []store.Meta, err error) {
	err = c.locked(func() error {
		removed, err = c.remove(idPrefixes)
		return err
	})
	return removed, err
}

func (c *Client) remove(idPrefixes []string) ([]store.Meta, error) {
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
