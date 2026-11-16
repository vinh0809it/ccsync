package store

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// git runs git in the clone.
func (s *Store) git(args ...string) (string, error) {
	return s.gitIn(s.Dir, args...)
}

func (s *Store) gitIn(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir,
		"-c", "user.name=ccsync", "-c", "user.email=ccsync@localhost",
		// A global signing setup would prompt or fail on store commits.
		"-c", "commit.gpgsign=false",
		// Blobs are binary; never let line-ending conversion touch them.
		"-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	// English messages: Push recognizes a lost race by git's wording.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if !s.Interactive {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Sync makes the clone an exact copy of the remote, discarding anything a
// failed or interrupted run left behind.
func (s *Store) Sync() error {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(s.path(".git", d)); err == nil {
			_, _ = s.git("rebase", "--abort")
		}
	}
	if _, err := s.git("fetch", "--quiet", "origin"); err != nil {
		return err
	}
	// Sync discards local changes: be sure this is a ccsync store first.
	if !s.upstreamIsStore() {
		return fmt.Errorf("%s does not track a ccsync store (no %s upstream) — forget it (ccsync store forget) and set it up again", s.Dir, markerFile)
	}
	if _, err := s.git("reset", "--hard", "--quiet", "@{u}"); err != nil {
		return err
	}
	_, err := s.git("clean", "-fdq")
	return err
}

// upstreamIsStore reports whether the fetched upstream holds a store marker.
func (s *Store) upstreamIsStore() bool {
	_, err := s.git("cat-file", "-e", "@{u}:"+markerFile)
	return err == nil
}

// Commit commits staged changes. Only the sessions tree and the key files
// are committed — a stray plaintext file elsewhere in the clone must never
// end up in the (possibly public) remote.
func (s *Store) Commit(message string) error {
	paths, err := s.storePaths()
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return ErrNothingToCommit
	}
	if _, err := s.git(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return err
	}
	out, err := s.git(append([]string{"status", "--porcelain", "--"}, paths...)...)
	if err != nil {
		return err
	}
	if out == "" {
		return ErrNothingToCommit
	}
	_, err = s.git("commit", "--quiet", "-m", message)
	return err
}

// storePaths returns the store's top-level paths that exist on disk or in
// the index; git rejects a pathspec that matches neither.
func (s *Store) storePaths() ([]string, error) {
	all := []string{sessionsDir, markerFile, checkFile, keyFile}
	tracked, err := s.git(append([]string{"ls-files", "--"}, all...)...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range all {
		_, statErr := os.Stat(s.path(p))
		if statErr == nil || containsPath(tracked, p) {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// containsPath reports whether the newline-separated file list has p or a
// file under p.
func containsPath(list, p string) bool {
	for _, f := range strings.Split(list, "\n") {
		if f == p || strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

// Push publishes local commits. It returns ErrRemoteMoved (wrapped) when
// another machine pushed first; the caller re-syncs and retries.
func (s *Store) Push() error {
	_, err := s.git("push", "--quiet")
	if err != nil && (strings.Contains(err.Error(), "fetch first") ||
		strings.Contains(err.Error(), "non-fast-forward")) {
		return fmt.Errorf("%w: %v", ErrRemoteMoved, err)
	}
	return err
}
