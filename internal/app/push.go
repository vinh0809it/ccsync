package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
)

// ActiveGrace is how recently a session may have been modified before it
// counts as live (Claude Code may still be appending to it). The guard is
// enforced here — not just in front-end code — so no code path can push a
// half-written session by accident.
const ActiveGrace = 30 * time.Second

// PushOptions relax Push's safety checks.
type PushOptions struct {
	// IncludeActive pushes sessions modified within ActiveGrace.
	IncludeActive bool
	// Overwrite replaces store copies that have messages the local copy
	// lacks (newer, diverged, or unreadable). In a git store the replaced
	// version stays in the history.
	Overwrite bool
}

// PushResult reports what Push did.
type PushResult struct {
	Pushed   []store.Meta
	Skipped  []string          // ids whose store copy already has the same entries
	Rejected []Rejection       // not pushed: it would lose messages from the store copy
	Active   []session.Session // not pushed: modified within ActiveGrace
}

// Rejection is a session Push refused to overwrite in the store.
type Rejection struct {
	Local    session.Session
	Store    store.Meta       // the store copy
	Relation session.Relation // of the local copy to the store copy: Behind, Diverged or Unknown
}

type pushItem struct {
	session    session.Session
	transcript []byte
	meta       store.Meta
	payload    []byte
}

// batchBytes is how much session data one push commit holds at most
// (a session larger than that gets a commit of its own).
const batchBytes = 64 << 20

// Push bundles the given local sessions into the store and publishes them,
// like a fast-forward git push: a store copy is replaced only if the local
// copy has all of its entries and more. Sessions whose store copy is newer,
// diverged or not comparable are reported in Rejected unless
// opts.Overwrite is set; up-to-date ones in Skipped; live ones (modified
// within ActiveGrace) in Active unless opts.IncludeActive is set.
// fallbackCwd is used when a transcript records no cwd.
func (c *Client) Push(sessions []session.Session, fallbackCwd string, opts PushOptions) (res PushResult, err error) {
	err = c.locked(func() error {
		res, err = c.push(sessions, fallbackCwd, opts)
		return err
	})
	return res, err
}

func (c *Client) push(sessions []session.Session, fallbackCwd string, opts PushOptions) (PushResult, error) {
	if len(sessions) == 0 {
		return PushResult{}, fmt.Errorf("nothing to push")
	}
	// Re-read every session: the caller's listing may predate new activity.
	var active []session.Session
	fresh := make([]session.Session, 0, len(sessions))
	for _, s := range sessions {
		s, err := session.Load(s.File)
		if err != nil && s.Mtime.IsZero() {
			return PushResult{}, err // unreadable; a parse problem alone still pushes
		}
		if time.Since(s.Mtime) < ActiveGrace && !opts.IncludeActive {
			active = append(active, s)
			continue
		}
		fresh = append(fresh, s)
	}
	for attempt := 1; ; attempt++ {
		res, staged, err := c.stagePush(fresh, fallbackCwd, opts)
		res.Active = active
		if err != nil || staged == 0 {
			return res, err
		}
		beforePush()
		c.progress("Uploading files", 0, 0)
		err = c.store.PushProgress(func(p store.UploadProgress) {
			// Git counts files (blobs and folders), not sessions.
			step := "Uploading files"
			if p.Parts > 1 {
				step = fmt.Sprintf("Uploading files, part %d of %d", p.Part, p.Parts)
			}
			c.progress(step, p.Done, p.Total)
		})
		// A failed push leaves unpublished commits that the next sync
		// discards, so a later run can never mistake them for published.
		if err == nil || !errors.Is(err, store.ErrRemoteMoved) || attempt == publishAttempts {
			return res, err
		}
	}
}

// stagePush encrypts sessions into a freshly synced store, one at a time,
// committing every batchBytes of session data so a commit stays a
// reasonable size. A store copy is replaced only as Push allows. It
// returns how many sessions were committed; nothing is uploaded yet.
func (c *Client) stagePush(sessions []session.Session, fallbackCwd string, opts PushOptions) (PushResult, int, error) {
	var res PushResult
	if err := c.sync(); err != nil {
		return res, 0, err
	}
	staged, pending := 0, 0
	var pendingBytes int64
	commit := func() error {
		if pending == 0 {
			return nil
		}
		// Commit messages are world-readable on a public remote: no
		// hostname, no titles — those live only in the encrypted metadata.
		if err := c.store.Commit(fmt.Sprintf("ccsync: push %d session(s)", pending)); err != nil {
			return err
		}
		staged += pending
		pending, pendingBytes = 0, 0
		return nil
	}
	host, _ := os.Hostname()
	// First drop what the store already has (same entries, by the
	// fingerprint pushes record): those need no packing or encrypting.
	var changed []session.Session
	for i, s := range sessions {
		c.progress("Checking", i, len(sessions))
		if existing, found, err := c.store.Lookup(c.key, s.ID); err == nil && found && existing.Entries != "" {
			if transcript, err := os.ReadFile(s.File); err == nil && session.Fingerprint(transcript) == existing.Entries {
				res.Skipped = append(res.Skipped, s.ID)
				continue
			}
		}
		changed = append(changed, s)
	}
	for i, s := range changed {
		c.progress("Encrypting", i, len(changed))
		it, err := bundle(s, fallbackCwd, host)
		if err != nil {
			return res, 0, err
		}
		// An unreadable store entry (e.g. from an outdated ccsync) can't
		// be compared; replacing it is the fix.
		existing, found, err := c.store.Lookup(c.key, it.meta.SessionID)
		found = found && err == nil
		if found && existing.ContentHash == it.meta.ContentHash {
			res.Skipped = append(res.Skipped, it.meta.SessionID)
			continue
		}
		if found {
			rel, err := c.compareWithStore(it.transcript, existing)
			if err != nil {
				rel = session.Unknown // unreadable store copy: needs Overwrite
			}
			switch {
			case rel == session.Same:
				// The copies differ only in relocated paths or bookkeeping,
				// e.g. a session pulled to another path and not continued.
				res.Skipped = append(res.Skipped, it.meta.SessionID)
				continue
			case rel != session.Ahead && !opts.Overwrite:
				res.Rejected = append(res.Rejected, Rejection{Local: it.session, Store: existing, Relation: rel})
				continue
			}
		}
		it.meta.PushedAt = time.Now().UTC()
		if err := c.store.Put(c.key, it.meta, it.payload); err != nil {
			return res, 0, err
		}
		res.Pushed = append(res.Pushed, it.meta)
		pending++
		if pendingBytes += s.Size; pendingBytes >= batchBytes {
			if err := commit(); err != nil {
				return res, 0, err
			}
		}
	}
	err := commit()
	return res, staged, err
}

// compareWithStore relates a local transcript to the store copy meta
// describes, decrypting that copy only when their fingerprints differ.
func (c *Client) compareWithStore(transcript []byte, meta store.Meta) (session.Relation, error) {
	if meta.Entries != "" && session.Fingerprint(transcript) == meta.Entries {
		return session.Same, nil
	}
	payload, err := c.store.OpenPayload(c.key, meta.SessionID)
	if err != nil {
		return 0, err
	}
	defer payload.Close()
	stored, err := session.Unpack(payload)
	if err != nil {
		return 0, fmt.Errorf("session %s: %w", meta.SessionID, err)
	}
	return session.Compare(transcript, stored.Transcript), nil
}

// bundle reads and packs one session for the store.
func bundle(s session.Session, fallbackCwd, host string) (pushItem, error) {
	contents, err := session.ReadContents(s)
	if err != nil {
		return pushItem{}, err
	}
	payload, hash, err := session.Pack(contents)
	if err != nil {
		return pushItem{}, err
	}
	cwd := s.Cwd
	if cwd == "" {
		cwd = fallbackCwd
	}
	return pushItem{session: s, transcript: contents.Transcript, payload: payload, meta: store.Meta{
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
		Entries:       session.Fingerprint(contents.Transcript),
	}}, nil
}
