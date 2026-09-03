// Package app holds the push/pull operations shared by the CLI and the GUI,
// so both front-ends go through exactly the same code path.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ccsync/internal/session"
	"ccsync/internal/store"
)

// ActiveGrace is how recently a transcript may have been modified before
// it counts as live (Claude Code may still be appending to it). The guard
// is enforced here — not just in front-end code — so no code path can push
// a half-written session by accident.
const ActiveGrace = 30 * time.Second

// Push encrypts the given local sessions into the store and pushes the
// commit. fallbackCwd is used when a transcript carries no cwd of its own.
// Sessions modified within ActiveGrace are refused unless force is set;
// sessions whose content is already in the store are skipped (their ids
// are returned in skipped) so repeated pushes don't grow the repo.
func Push(pass string, sessions []session.Session, fallbackCwd string, force bool) (pushed []store.Meta, skipped []string, err error) {
	if len(sessions) == 0 {
		return nil, nil, fmt.Errorf("nothing to push")
	}
	if err := store.Sync(); err != nil {
		return nil, nil, err
	}
	if err := store.VerifyPassphrase(pass); err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	for _, s := range sessions {
		// Stat fresh: the caller's listing may predate new activity.
		info, err := os.Stat(s.File)
		if err != nil {
			return nil, nil, err
		}
		if time.Since(info.ModTime()) < ActiveGrace && !force {
			return nil, nil, fmt.Errorf("session %s looks active (modified %s ago) — close it or force the push", s.ID, time.Since(info.ModTime()).Round(time.Second))
		}
		data, err := os.ReadFile(s.File)
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		if existing, found, err := store.GetMeta(pass, s.ID); err == nil && found && existing.ContentHash == hash {
			skipped = append(skipped, s.ID)
			continue
		}
		cwd := s.Cwd
		if cwd == "" {
			cwd = fallbackCwd
		}
		meta := store.Meta{
			SessionID:     s.ID,
			Title:         s.Title,
			ProjectLabel:  filepath.Base(cwd),
			OriginalCwd:   cwd,
			Hostname:      host,
			ClaudeVersion: s.Version,
			SessionMtime:  info.ModTime(),
			Size:          info.Size(),
			ContentHash:   hash,
			PushedAt:      time.Now().UTC(),
		}
		if err := store.Put(pass, meta, data); err != nil {
			return nil, nil, err
		}
		pushed = append(pushed, meta)
	}
	if len(pushed) == 0 {
		return nil, skipped, nil
	}
	// Commit messages are world-readable on a public remote: no hostname,
	// no titles — those live only inside the encrypted metadata.
	msg := fmt.Sprintf("ccsync: push %d session(s)", len(pushed))
	if err := store.Commit(msg); err != nil {
		return nil, nil, err
	}
	return pushed, skipped, nil
}

// Pull fetches one session by (possibly abbreviated) id, rewrites its
// structural cwd fields to dest, and installs it locally. It returns the
// session metadata and the path of the installed transcript.
func Pull(pass, idPrefix, dest string, force bool) (store.Meta, string, error) {
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return store.Meta{}, "", err
	}
	if err := store.Sync(); err != nil {
		return store.Meta{}, "", err
	}
	if err := store.VerifyPassphrase(pass); err != nil {
		return store.Meta{}, "", err
	}
	meta, data, err := store.Get(pass, idPrefix)
	if err != nil {
		return store.Meta{}, "", err
	}
	data = session.RewriteCwd(data, meta.OriginalCwd, destAbs)
	target, err := session.Install(meta.SessionID, data, destAbs, force)
	if err != nil {
		return store.Meta{}, "", err
	}
	return meta, target, nil
}
