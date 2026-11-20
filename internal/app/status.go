package app

import (
	"context"
	"os"
	"time"

	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
)

// cachedRelation is a Status result, valid while neither copy changes.
type cachedRelation struct {
	mtime     time.Time // local copy
	size      int64     // local copy
	storeHash string    // store copy's ContentHash
	rel       session.Relation
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
			if cached && hit.mtime.Equal(s.Mtime) && hit.size == s.Size && hit.storeHash == meta.ContentHash {
				out[s.ID] = hit.rel
				continue
			}
			// Status never downloads a bundle: a copy without a fingerprint
			// (pushed by an older ccsync) stays Unknown until a push records
			// one.
			rel := session.Unknown
			if transcript, err := os.ReadFile(s.File); err == nil && meta.Entries != "" {
				if r, err := c.compareWithStore(transcript, meta); err == nil {
					rel = r
				}
			}
			c.status[s.ID] = cachedRelation{s.Mtime, s.Size, meta.ContentHash, rel}
			out[s.ID] = rel
		}
		return nil
	})
	return out, err
}
