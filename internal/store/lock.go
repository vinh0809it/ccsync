package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockWait is how long an operation waits for another ccsync process (say
// the GUI while a CLI command runs) to finish with the same store.
const lockWait = 2 * time.Minute

// errLocked means another process holds the lock (from tryLock).
var errLocked = errors.New("locked")

// lockPath is where the lock for the named store lives: in this machine's
// data directory, never inside a shared folder.
func lockPath(name string) (string, error) {
	d, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "locks", name+".lock"), nil
}

// Lock takes this machine's exclusive lock on the store, waiting for other
// ccsync processes, and returns its release function. Every operation that
// syncs, writes or publishes runs under it.
func (s *Store) Lock() (unlock func(), err error) {
	if s.lockFile == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.lockFile), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := tryLock(f)
		if err == nil {
			return func() { unlockFile(f); f.Close() }, nil
		}
		if !errors.Is(err, errLocked) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, errLocked) {
				return nil, fmt.Errorf("another ccsync is using this store (waited %s) — close it and retry", lockWait)
			}
			return nil, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}
