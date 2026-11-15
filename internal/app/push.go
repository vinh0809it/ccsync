package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vinh-vh/ccsync/internal/session"
	"github.com/vinh-vh/ccsync/internal/store"
)

// ActiveGrace is how recently a session may have been modified before it
// counts as live (Claude Code may still be appending to it). The guard is
// enforced here — not just in front-end code — so no code path can push a
// half-written session by accident.
const ActiveGrace = 30 * time.Second

// PushResult reports what Push did.
type PushResult struct {
	Pushed  []store.Meta
	Skipped []string // ids already in the store with identical content
}

type pushItem struct {
	meta    store.Meta
	payload []byte
}

// Push bundles the given local sessions into the store and publishes them.
// fallbackCwd is used when a transcript carries no cwd of its own.
// Sessions modified within ActiveGrace are refused unless force is set;
// sessions whose content is already in the store are skipped, so repeated
// pushes don't grow the repo.
func (c *Client) Push(sessions []session.Session, fallbackCwd string, force bool) (PushResult, error) {
	if len(sessions) == 0 {
		return PushResult{}, fmt.Errorf("nothing to push")
	}
	items, err := bundleAll(sessions, fallbackCwd, force)
	if err != nil {
		return PushResult{}, err
	}
	var res PushResult
	err = c.publish(
		// Commit messages are world-readable on a public remote: no
		// hostname, no titles — those live only in the encrypted metadata.
		func(n int) string { return fmt.Sprintf("ccsync: push %d session(s)", n) },
		func() (int, error) {
			res = PushResult{}
			for _, it := range items {
				existing, found, err := c.store.Lookup(c.key, it.meta.SessionID)
				if err == nil && found && existing.ContentHash == it.meta.ContentHash {
					res.Skipped = append(res.Skipped, it.meta.SessionID)
					continue
				}
				it.meta.PushedAt = time.Now().UTC()
				if err := c.store.Put(c.key, it.meta, it.payload); err != nil {
					return 0, err
				}
				res.Pushed = append(res.Pushed, it.meta)
			}
			return len(res.Pushed), nil
		})
	if err != nil {
		return PushResult{}, err
	}
	return res, nil
}

// bundleAll re-reads and bundles each session, refusing live ones.
func bundleAll(sessions []session.Session, fallbackCwd string, force bool) ([]pushItem, error) {
	host, _ := os.Hostname()
	var items []pushItem
	for _, s := range sessions {
		// Re-read: the caller's listing may predate new activity.
		s, err := session.Load(s.File)
		if err != nil && s.Mtime.IsZero() {
			return nil, err // unreadable; a parse problem alone still pushes
		}
		if age := time.Since(s.Mtime); age < ActiveGrace && !force {
			return nil, fmt.Errorf("session %s looks active (modified %s ago) — close it or force the push", s.ID, age.Round(time.Second))
		}
		payload, hash, err := session.Bundle(s)
		if err != nil {
			return nil, err
		}
		cwd := s.Cwd
		if cwd == "" {
			cwd = fallbackCwd
		}
		items = append(items, pushItem{payload: payload, meta: store.Meta{
			SessionID:     s.ID,
			Title:         s.Title,
			ProjectLabel:  filepath.Base(cwd),
			OriginalCwd:   cwd,
			SessionDir:    s.Dir,
			Hostname:      host,
			ClaudeVersion: s.Version,
			SessionMtime:  s.Mtime,
			Size:          s.Size,
			ContentHash:   hash,
		}})
	}
	return items, nil
}
