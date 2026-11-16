package app

import (
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

// batchBytes bounds how much session data one publish holds in memory;
// larger pushes become several commits.
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
	var total PushResult
	fresh := make([]session.Session, 0, len(sessions))
	for _, s := range sessions {
		s, err := session.Load(s.File)
		if err != nil && s.Mtime.IsZero() {
			return PushResult{}, err // unreadable; a parse problem alone still pushes
		}
		if time.Since(s.Mtime) < ActiveGrace && !opts.IncludeActive {
			total.Active = append(total.Active, s)
			continue
		}
		fresh = append(fresh, s)
	}
	for _, batch := range batches(fresh, batchBytes) {
		items, err := bundleAll(batch, fallbackCwd)
		if err != nil {
			return total, err
		}
		res, err := c.pushBatch(items, opts)
		total.Pushed = append(total.Pushed, res.Pushed...)
		total.Skipped = append(total.Skipped, res.Skipped...)
		total.Rejected = append(total.Rejected, res.Rejected...)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// batches splits sessions into runs of at most limit bytes (a session
// larger than limit gets a batch of its own).
func batches(sessions []session.Session, limit int64) [][]session.Session {
	var out [][]session.Session
	var cur []session.Session
	var size int64
	for _, s := range sessions {
		if len(cur) > 0 && size+s.Size > limit {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, s)
		size += s.Size
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// pushBatch publishes one batch as one commit.
func (c *Client) pushBatch(items []pushItem, opts PushOptions) (PushResult, error) {
	var res PushResult
	err := c.publish(
		// Commit messages are world-readable on a public remote: no
		// hostname, no titles — those live only in the encrypted metadata.
		func(n int) string { return fmt.Sprintf("ccsync: push %d session(s)", n) },
		func() (int, error) {
			res = PushResult{}
			for _, it := range items {
				// An unreadable store entry (e.g. from an outdated ccsync)
				// can't be compared; replacing it is the fix.
				existing, found, err := c.store.Lookup(c.key, it.meta.SessionID)
				found = found && err == nil
				if found && existing.ContentHash == it.meta.ContentHash {
					res.Skipped = append(res.Skipped, it.meta.SessionID)
					continue
				}
				if found {
					rel, err := c.relationToStore(it, existing)
					if err != nil {
						rel = session.Unknown // unreadable store copy: needs Overwrite
					}
					switch {
					case rel == session.Same:
						// The copies differ only in relocated paths or
						// bookkeeping, e.g. a session pulled to another path
						// and not continued.
						res.Skipped = append(res.Skipped, it.meta.SessionID)
						continue
					case rel != session.Ahead && !opts.Overwrite:
						res.Rejected = append(res.Rejected, Rejection{Local: it.session, Store: existing, Relation: rel})
						continue
					}
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

// relationToStore compares a local session with its store copy.
func (c *Client) relationToStore(it pushItem, existing store.Meta) (session.Relation, error) {
	payload, err := c.store.OpenPayload(c.key, existing.SessionID)
	if err != nil {
		return 0, err
	}
	defer payload.Close()
	stored, err := session.Unpack(payload)
	if err != nil {
		return 0, fmt.Errorf("session %s: %w", existing.SessionID, err)
	}
	return session.Compare(it.transcript, stored.Transcript), nil
}

// bundleAll reads and bundles each session.
func bundleAll(sessions []session.Session, fallbackCwd string) ([]pushItem, error) {
	host, _ := os.Hostname()
	var items []pushItem
	for _, s := range sessions {
		contents, err := session.ReadContents(s)
		if err != nil {
			return nil, err
		}
		payload, hash, err := session.Pack(contents)
		if err != nil {
			return nil, err
		}
		cwd := s.Cwd
		if cwd == "" {
			cwd = fallbackCwd
		}
		items = append(items, pushItem{session: s, transcript: contents.Transcript, payload: payload, meta: store.Meta{
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
