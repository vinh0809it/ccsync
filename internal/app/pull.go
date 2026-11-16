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
	destAbs, err := session.ResolveProject(dest)
	if err != nil {
		return PullResult{}, err
	}
	if err := c.store.Sync(); err != nil {
		return PullResult{}, err
	}
	meta, err := c.store.Find(c.key, idPrefix)
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
