package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
)

// cachedRelation is a Status result, valid while neither copy changes.
type cachedRelation struct {
	mtime    time.Time // local copy
	size     int64     // local copy
	storeKey string    // store copy's bundle and what its metadata records
	rel      session.Relation
}

// Status relates each local session that the store also holds to its store
// copy, as Push would: Same, Ahead (push it), Behind (pull it), Diverged
// or Unknown. Sessions only on one side are left out. It reads the store
// as last synced (call List first) and remembers results until either copy
// changes, so asking again is cheap. Cancelling ctx stops it early with
// ctx's error; what was compared so far stays cached.
func (c *Client) Status(ctx context.Context, locals []session.Session) (map[string]session.Relation, error) {
	out := map[string]session.Relation{}
	err := c.locked(func() error {
		metas, _, err := c.store.List(c.key)
		if err != nil {
			return err
		}
		byID := make(map[string]store.Meta, len(metas))
		for _, m := range metas {
			byID[m.SessionID] = m
		}
		if c.status == nil {
			c.status = map[string]cachedRelation{}
		}
		for i, s := range locals {
			if err := ctx.Err(); err != nil {
				return err
			}
			c.progress("Comparing", i, len(locals))
			meta, found := byID[s.ID]
			if !found {
				continue
			}
			hit, cached := c.status[s.ID]
			if cached && hit.mtime.Equal(s.Mtime) && hit.size == s.Size && hit.storeKey == storeKey(meta) {
				out[s.ID] = hit.rel
				continue
			}
			// Status never downloads a bundle: it compares with the
			// metadata's record of entries. A copy pushed before that record
			// existed stays Unknown (unless plainly in sync) until a push
			// adds it.
			rel := session.Unknown
			if transcript, err := os.ReadFile(s.File); err == nil && (meta.EntrySet != "" ||
				(meta.Entries != "" && session.Fingerprint(transcript) == meta.Entries)) {
				if r, err := c.compareWithStore(transcript, meta); err == nil {
					rel = r
				}
			}
			c.status[s.ID] = cachedRelation{s.Mtime, s.Size, storeKey(meta), rel}
			out[s.ID] = rel
		}
		return nil
	})
	return out, err
}

// storeKey changes when a store copy's bundle or its metadata's record of
// the entries changes (a push backfilling an older copy).
func storeKey(m store.Meta) string {
	return fmt.Sprintf("%s/%d/%s", m.ContentHash, m.Version, m.Entries)
}
