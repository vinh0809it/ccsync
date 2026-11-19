package app

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
)

// PullResult reports what Pull did.
type PullResult struct {
	Meta store.Meta
	Dest string // resolved project path
	session.InstallResult
}

// Pull fetches one session by (possibly abbreviated) id and installs it for
// the project at dest, relocating its paths there. See session.Install for
// how an existing local copy is treated.
//
// A local copy that Claude Code is writing (modified within ActiveGrace)
// is not replaced unless force is set: the running session would keep
// appending to it from its own, older state.
func (c *Client) Pull(idPrefix, dest string, force bool) (res PullResult, err error) {
	err = c.locked(func() error {
		res, err = c.pull(idPrefix, dest, force)
		return err
	})
	return res, err
}

func (c *Client) pull(idPrefix, dest string, force bool) (PullResult, error) {
	if err := c.store.Sync(); err != nil {
		return PullResult{}, err
	}
	meta, err := c.store.Find(c.key, idPrefix)
	if err != nil {
		return PullResult{}, err
	}
	return c.pullMeta(meta, dest, force)
}

// pullMeta installs one store session for the project at dest. The caller
// holds the lock and has synced.
func (c *Client) pullMeta(meta store.Meta, dest string, force bool) (PullResult, error) {
	destAbs, err := session.ResolveProject(dest)
	if err != nil {
		return PullResult{}, err
	}
	target, err := session.Target(meta.SessionID, destAbs)
	if err != nil {
		return PullResult{}, err
	}
	if info, err := os.Stat(target); err == nil && time.Since(info.ModTime()) < ActiveGrace && !force {
		return PullResult{}, fmt.Errorf("%s looks open in Claude Code (modified %s ago) — close it first, or pull with force",
			meta.SessionID, time.Since(info.ModTime()).Round(time.Second))
	}
	payload, err := c.store.OpenPayload(c.key, meta.SessionID)
	if err != nil {
		return PullResult{}, err
	}
	contents, err := session.Unpack(payload)
	payload.Close()
	if err != nil {
		return PullResult{}, fmt.Errorf("session %s: %w", meta.SessionID, err)
	}
	res, err := session.Install(meta.SessionID, contents,
		session.Origin{Cwd: meta.OriginalCwd, SessionDir: meta.SessionDir, Mtime: meta.SessionMtime}, destAbs, force)
	switch {
	case errors.Is(err, session.ErrLocalAhead):
		return PullResult{}, fmt.Errorf("the local copy of %s has messages the store copy lacks — push it instead, or pull with force to replace it (the local copy is kept as .bak files)", meta.SessionID)
	case errors.Is(err, session.ErrUnknown):
		return PullResult{}, fmt.Errorf("the local copy of %s and the store copy can't be compared (unrecognized transcript format) — pull with force to take the store copy (the local copy is kept as .bak files)", meta.SessionID)
	case errors.Is(err, session.ErrDiverged):
		return PullResult{}, fmt.Errorf("%s was continued both here and on %s (pushed %s) — pull with force to take the store copy (the local copy is kept as .bak files), or push with force to keep yours",
			meta.SessionID, meta.Hostname, meta.PushedAt.Local().Format("2006-01-02 15:04"))
	}
	if err != nil {
		return PullResult{}, err
	}
	return PullResult{Meta: meta, Dest: destAbs, InstallResult: res}, nil
}

// PlannedPull is where a pull would install a store session.
type PlannedPull struct {
	Meta        store.Meta
	Dest        string // project path on this machine
	DestExists  bool   // the project folder exists here
	LocalExists bool   // this machine already has a copy of the session
}

// PlanPull works out where each session would go, without changing
// anything: its original project path, moved by m. With no ids it plans
// every session in the store.
func (c *Client) PlanPull(ids []string, m PathMap) (plan []PlannedPull, err error) {
	err = c.locked(func() error {
		if err := c.store.Sync(); err != nil {
			return err
		}
		metas, _, err := c.resolve(ids)
		if err != nil {
			return err
		}
		for _, meta := range metas {
			p := PlannedPull{Meta: meta}
			if meta.OriginalCwd != "" {
				if p.Dest, err = session.ResolveProject(m.Apply(meta.OriginalCwd)); err != nil {
					return err
				}
				_, statErr := os.Stat(p.Dest)
				p.DestExists = statErr == nil
				if target, err := session.Target(meta.SessionID, p.Dest); err == nil {
					_, statErr := os.Stat(target)
					p.LocalExists = statErr == nil
				}
			}
			plan = append(plan, p)
		}
		return nil
	})
	return plan, err
}

// PullFailure is a session PullMany could not install.
type PullFailure struct {
	ID    string
	Title string
	Err   error
}

// PullManyResult reports what PullMany did.
type PullManyResult struct {
	Pulled    []PullResult // installed or updated
	Unchanged []PullResult // already up to date here
	Failed    []PullFailure
}

// PullMany installs several sessions (all of the store's when ids is
// empty), each for its original project path moved by m. A session that
// can't be installed — newer here, diverged, open in Claude Code, no
// recorded project — is reported in Failed; the others still go through.
// force applies to every session, as for Pull.
func (c *Client) PullMany(ids []string, m PathMap, force bool) (res PullManyResult, err error) {
	err = c.locked(func() error {
		if err := c.store.Sync(); err != nil {
			return err
		}
		metas, unreadable, err := c.resolve(ids)
		if err != nil {
			return err
		}
		for _, id := range unreadable {
			res.Failed = append(res.Failed, PullFailure{ID: id, Err: fmt.Errorf("can't be read (pushed by an outdated ccsync? run: ccsync migrate)")})
		}
		for _, meta := range metas {
			if meta.OriginalCwd == "" {
				res.Failed = append(res.Failed, PullFailure{meta.SessionID, meta.Title,
					fmt.Errorf("no project path was recorded — pull it on its own and choose one")})
				continue
			}
			r, err := c.pullMeta(meta, m.Apply(meta.OriginalCwd), force)
			switch {
			case err != nil:
				res.Failed = append(res.Failed, PullFailure{meta.SessionID, meta.Title, err})
			case r.Unchanged:
				res.Unchanged = append(res.Unchanged, r)
			default:
				res.Pulled = append(res.Pulled, r)
			}
		}
		return nil
	})
	return res, err
}

// resolve turns session ids or prefixes into store metadata; no ids means
// every readable session (unreadable ones are returned apart).
func (c *Client) resolve(ids []string) (metas []store.Meta, unreadable []string, err error) {
	if len(ids) == 0 {
		return c.store.List(c.key)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		meta, err := c.store.Find(c.key, id)
		if err != nil {
			return nil, nil, err
		}
		if !seen[meta.SessionID] {
			seen[meta.SessionID] = true
			metas = append(metas, meta)
		}
	}
	return metas, nil, nil
}

// ResumeCommand is the shell command that resumes the pulled session.
func (r PullResult) ResumeCommand() string {
	return fmt.Sprintf("cd %s && claude --resume %s", shellQuote(r.Dest), r.Meta.SessionID)
}

// shellQuote quotes a path for the user's shell when it needs it, so a
// printed command can be pasted as is.
func shellQuote(p string) string {
	if runtime.GOOS == "windows" {
		return `"` + p + `"`
	}
	if strings.IndexFunc(p, func(r rune) bool {
		return !(r == '/' || r == '-' || r == '_' || r == '.' || r == '~' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
