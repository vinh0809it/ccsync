package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// transport is where a store's files are shared between machines. The
// Store reads and writes its files in Store.Dir; a transport makes that
// directory reflect the shared copy (sync) and shares local changes
// (commit, push).
type transport interface {
	// open prepares Store.Dir for the store at location: a repo URL to
	// clone, or a folder path.
	open(location string) error
	// sync brings Store.Dir up to date, discarding leftovers of failed runs.
	sync() error
	// commit records the changes written since the last sync;
	// ErrNothingToCommit if there are none.
	commit(message string) error
	// push shares committed changes; ErrRemoteMoved (wrapped) when another
	// machine was first.
	push() error
	// forget removes what this machine keeps of the store — never the
	// shared copy.
	forget() error
}

// Kind is how a store is shared.
type Kind string

const (
	KindGit    Kind = "git"    // a git repository
	KindFolder Kind = "folder" // a folder kept in sync by another tool
)

// folderTransport keeps the store in a folder that a sync service (Dropbox,
// iCloud Drive, Google Drive, OneDrive, Syncthing, ...) or a network share
// keeps in step across machines. Store.Dir is that folder: every file is
// written atomically, so other machines see either the old or the new
// file, never part of one. There is no history: a replaced version is
// gone (most sync services keep their own file history).
type folderTransport struct{ s *Store }

func (f folderTransport) open(location string) error {
	if location != f.s.Dir {
		return fmt.Errorf("internal error: folder store at %s opened as %s", f.s.Dir, location)
	}
	info, err := os.Stat(f.s.Dir)
	switch {
	case os.IsNotExist(err):
		return os.MkdirAll(f.s.Dir, 0o700)
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s is not a folder", f.s.Dir)
	}
	// An existing folder must be a store or empty — never adopt (and later
	// write into) some unrelated folder.
	if _, err := os.Stat(f.s.path(markerFile)); err == nil {
		return nil
	}
	entries, err := os.ReadDir(f.s.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") { // .DS_Store, desktop.ini-like dotfiles
			return fmt.Errorf("%s is not empty and holds no ccsync store — pick an empty folder", f.s.Dir)
		}
	}
	return nil
}

// sync removes temp files an interrupted run of this machine left behind;
// the sync service does the rest.
func (f folderTransport) sync() error {
	if _, err := os.Stat(f.s.path(markerFile)); err != nil {
		return fmt.Errorf("no ccsync store in %s (is the folder synced to this machine yet?)", f.s.Dir)
	}
	stale, _ := filepath.Glob(f.s.path(sessionsDir, "*", tempPrefix+"*"))
	for _, p := range stale {
		_ = os.Remove(p)
	}
	return nil
}

func (folderTransport) commit(string) error { return nil } // writes are already in place
func (folderTransport) push() error         { return nil } // the sync service ships them

// forget leaves the folder alone: it is the shared store itself.
func (folderTransport) forget() error { return nil }
