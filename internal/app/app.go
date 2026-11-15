// Package app holds the operations shared by the CLI and the GUI, so both
// front-ends go through exactly the same code path.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vinhvh0809/ccsync/internal/crypto"
	"github.com/vinhvh0809/ccsync/internal/session"
	"github.com/vinhvh0809/ccsync/internal/store"
)

// ActiveGrace is how recently a session may have been modified before it
// counts as live (Claude Code may still be appending to it). The guard is
// enforced here — not just in front-end code — so no code path can push a
// half-written session by accident.
const ActiveGrace = 30 * time.Second

// pushAttempts bounds retries when other machines keep pushing first.
const pushAttempts = 3

// beforePush runs between commit and push; tests use it to simulate
// another machine pushing in that window.
var beforePush = func() {}

// Unlock syncs the store and returns its key.
func Unlock(pass string) (*crypto.Key, error) {
	if err := store.Sync(); err != nil {
		return nil, err
	}
	return store.Unlock(pass)
}

// Push encrypts the given local sessions into the store and pushes the
// commit. fallbackCwd is used when a transcript carries no cwd of its own.
// Sessions modified within ActiveGrace are refused unless force is set;
// sessions whose content is already in the store are skipped (their ids
// are returned in skipped) so repeated pushes don't grow the repo.
func Push(key *crypto.Key, sessions []session.Session, fallbackCwd string, force bool) (pushed []store.Meta, skipped []string, err error) {
	if len(sessions) == 0 {
		return nil, nil, fmt.Errorf("nothing to push")
	}
	type item struct {
		meta    store.Meta
		payload []byte
	}
	host, _ := os.Hostname()
	var items []item
	for _, s := range sessions {
		// Re-read: the caller's listing may predate new activity.
		s, err := session.Load(s.File)
		if err != nil && s.Mtime.IsZero() {
			return nil, nil, err // unreadable; a parse problem alone still pushes
		}
		if age := time.Since(s.Mtime); age < ActiveGrace && !force {
			return nil, nil, fmt.Errorf("session %s looks active (modified %s ago) — close it or force the push", s.ID, age.Round(time.Second))
		}
		payload, hash, err := session.Bundle(s)
		if err != nil {
			return nil, nil, err
		}
		cwd := s.Cwd
		if cwd == "" {
			cwd = fallbackCwd
		}
		items = append(items, item{payload: payload, meta: store.Meta{
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

	for attempt := 1; ; attempt++ {
		if err := store.Sync(); err != nil {
			return nil, nil, err
		}
		pushed, skipped = nil, nil
		for _, it := range items {
			if existing, found, err := store.GetMeta(key, it.meta.SessionID); err == nil && found && existing.ContentHash == it.meta.ContentHash {
				skipped = append(skipped, it.meta.SessionID)
				continue
			}
			it.meta.PushedAt = time.Now().UTC()
			if err := store.Put(key, it.meta, it.payload); err != nil {
				return nil, nil, err
			}
			pushed = append(pushed, it.meta)
		}
		if len(pushed) == 0 {
			return nil, skipped, nil
		}
		// Commit messages are world-readable on a public remote: no
		// hostname, no titles — those live only inside the encrypted metadata.
		if err := store.Commit(fmt.Sprintf("ccsync: push %d session(s)", len(pushed))); err != nil {
			return nil, nil, err
		}
		beforePush()
		err := store.Push()
		if err == nil {
			return pushed, skipped, nil
		}
		// The unpushed commit is discarded by the next Sync, so a failure
		// here can never make a later push report "already up to date".
		if !errors.Is(err, store.ErrRemoteMoved) || attempt == pushAttempts {
			return nil, nil, err
		}
	}
}

// PullResult reports what Pull did.
type PullResult struct {
	Meta store.Meta
	Dest string // resolved project path
	session.InstallResult
}

// Pull fetches one session by (possibly abbreviated) id and installs it for
// the project at dest, relocating its paths there.
func Pull(key *crypto.Key, idPrefix, dest string, force bool) (PullResult, error) {
	destAbs, err := session.ResolveProject(dest)
	if err != nil {
		return PullResult{}, err
	}
	if err := store.Sync(); err != nil {
		return PullResult{}, err
	}
	meta, payload, err := store.Get(key, idPrefix)
	if err != nil {
		return PullResult{}, err
	}
	contents, err := session.Unpack(payload)
	if err != nil {
		return PullResult{}, fmt.Errorf("session %s: %w", meta.SessionID, err)
	}
	res, err := session.Install(meta.SessionID, contents,
		session.Origin{Cwd: meta.OriginalCwd, SessionDir: meta.SessionDir}, destAbs, force)
	if errors.Is(err, session.ErrLocalDiffers) {
		return PullResult{}, fmt.Errorf("%s differs from the store copy (pushed %s from %s) — if the local one is newer, push it instead; to replace it, pull with force (the local copy is kept as a .bak file)",
			res.Target, meta.PushedAt.Local().Format("2006-01-02 15:04"), meta.Hostname)
	}
	if err != nil {
		return PullResult{}, err
	}
	return PullResult{Meta: meta, Dest: destAbs, InstallResult: res}, nil
}

// Migrate upgrades the store to the current format.
func Migrate(pass string) (int, error) {
	return store.Migrate(pass, func(transcript []byte) ([]byte, string, error) {
		return session.Pack(session.Contents{Transcript: transcript})
	})
}
