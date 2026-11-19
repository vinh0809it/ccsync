package store

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// gitTransport keeps the store in a git repo; Store.Dir is a local clone
// that is reset to the remote before every operation.
type gitTransport struct{ s *Store }

// git runs git in the clone.
func (g gitTransport) git(args ...string) (string, error) {
	return g.gitIn(g.s.Dir, args...)
}

func (g gitTransport) gitIn(dir string, args ...string) (string, error) {
	out, err := g.command(dir, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// command prepares git to run in dir with the store's settings.
func (g gitTransport) command(dir string, args ...string) *exec.Cmd {
	full := append([]string{"-C", dir,
		"-c", "user.name=ccsync", "-c", "user.email=ccsync@localhost",
		// A global signing setup would prompt or fail on store commits.
		"-c", "commit.gpgsign=false",
		// Blobs are binary; never let line-ending conversion touch them.
		"-c", "core.autocrlf=false",
		// The user's global hooks are for their code, not this clone.
		"-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := exec.Command("git", full...)
	// English messages: push recognizes a lost race by git's wording.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if !g.s.Interactive {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	}
	return cmd
}

// open clones repoURL (or adopts an existing clone of it) and brings it up
// to date:
//   - a store upstream: sync;
//   - another non-empty repo (e.g. created with a README): fast-forward to
//     it, keeping its files, and build the store on top;
//   - no upstream: the remote must be empty, and whatever an earlier init
//     left locally (its first push may have been rejected) never reached
//     it, so it is discarded.
func (g gitTransport) open(repoURL string) error {
	if _, err := os.Stat(g.s.path(".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(g.s.Dir), 0o700); err != nil {
			return err
		}
		if _, err := g.gitIn(".", "clone", "--quiet", "--", repoURL, g.s.Dir); err != nil {
			return err
		}
	} else {
		// The clone must track the same remote, or every later push would
		// silently land in the old repo.
		current, err := g.git("remote", "get-url", "origin")
		if err != nil {
			return err
		}
		if current != repoURL {
			return fmt.Errorf("store clone %s tracks %s, not %s — forget that store first", g.s.Dir, current, repoURL)
		}
	}
	if _, err := g.git("rev-parse", "--verify", "--quiet", "@{u}"); err == nil {
		if _, err := g.git("fetch", "--quiet", "origin"); err != nil {
			return err
		}
		if g.upstreamIsStore() {
			return g.sync()
		}
		_, err := g.git("merge", "--ff-only", "--quiet", "@{u}")
		return err
	}
	heads, err := g.git("ls-remote", "--heads", "origin")
	if err != nil {
		return err
	}
	if heads != "" {
		return fmt.Errorf("store clone %s is out of step with the remote — forget that store and set it up again", g.s.Dir)
	}
	_, _ = g.git("update-ref", "-d", "HEAD")
	for _, f := range []string{markerFile, checkFile, keyFile} {
		_ = os.Remove(g.s.path(f))
	}
	return nil
}

// sync makes the clone an exact copy of the remote, discarding anything a
// failed or interrupted run left behind.
func (g gitTransport) sync() error {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(g.s.path(".git", d)); err == nil {
			_, _ = g.git("rebase", "--abort")
		}
	}
	if _, err := g.git("fetch", "--quiet", "origin"); err != nil {
		return err
	}
	// sync discards local changes: be sure this is a ccsync store first.
	if !g.upstreamIsStore() {
		return fmt.Errorf("%s does not track a ccsync store (no %s upstream) — forget it (ccsync store forget) and set it up again", g.s.Dir, markerFile)
	}
	if _, err := g.git("reset", "--hard", "--quiet", "@{u}"); err != nil {
		return err
	}
	_, err := g.git("clean", "-fdq")
	return err
}

// upstreamIsStore reports whether the fetched upstream holds a store marker.
func (g gitTransport) upstreamIsStore() bool {
	_, err := g.git("cat-file", "-e", "@{u}:"+markerFile)
	return err == nil
}

// commit commits the store's files. Only the sessions tree and the key
// files are committed — a stray plaintext file elsewhere in the clone must
// never end up in the remote.
func (g gitTransport) commit(message string) error {
	paths, err := g.storePaths()
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return ErrNothingToCommit
	}
	if _, err := g.git(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return err
	}
	out, err := g.git(append([]string{"status", "--porcelain", "--"}, paths...)...)
	if err != nil {
		return err
	}
	if out == "" {
		return ErrNothingToCommit
	}
	_, err = g.git("commit", "--quiet", "-m", message)
	return err
}

// storePaths returns the store's top-level paths that exist on disk or in
// the index; git rejects a pathspec that matches neither.
func (g gitTransport) storePaths() ([]string, error) {
	all := []string{sessionsDir, markerFile, checkFile, keyFile}
	tracked, err := g.git(append([]string{"ls-files", "--"}, all...)...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range all {
		_, statErr := os.Stat(g.s.path(p))
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

// push publishes the current branch with an explicit refspec (independent
// of the user's push.default) and sets its upstream.
func (g gitTransport) push(progress func(done, total int)) error {
	var err error
	if progress == nil {
		_, err = g.git("push", "--quiet", "-u", "origin", "HEAD")
	} else {
		err = g.gitProgress(progress, "push", "--progress", "-u", "origin", "HEAD")
	}
	if err != nil && (strings.Contains(err.Error(), "fetch first") ||
		strings.Contains(err.Error(), "non-fast-forward")) {
		return fmt.Errorf("%w: %v", ErrRemoteMoved, err)
	}
	return err
}

// writingObjects matches git's upload progress: "Writing objects:  45% (9/20)".
var writingObjects = regexp.MustCompile(`Writing objects:\s+\d+% \((\d+)/(\d+)\)`)

// gitProgress runs git like git(), reporting its "Writing objects"
// progress as it streams by.
func (g gitTransport) gitProgress(progress func(done, total int), args ...string) error {
	cmd := g.command(g.s.Dir, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	var out strings.Builder
	sc := bufio.NewScanner(stderr)
	sc.Split(scanCRLF) // progress lines end in \r
	for sc.Scan() {
		line := sc.Text()
		if m := writingObjects.FindStringSubmatch(line); m != nil {
			done, _ := strconv.Atoi(m[1])
			total, _ := strconv.Atoi(m[2])
			progress(done, total)
			continue
		}
		out.WriteString(line + "\n")
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(out.String()))
	}
	return nil
}

// scanCRLF splits at \r or \n.
func scanCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// compact pushes a parentless commit of the current tree in place of the
// remote branch. The push is leased on the commit the clone was synced to,
// so a push another machine made meanwhile is never overwritten. The
// clone's old objects are then pruned.
func (g gitTransport) compact() (CompactResult, error) {
	var r CompactResult
	upstream, err := g.git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return r, err
	}
	branch, ok := strings.CutPrefix(upstream, "origin/")
	if !ok {
		return r, fmt.Errorf("store clone tracks %s, not a branch of origin", upstream)
	}
	old, err := g.git("rev-parse", "HEAD")
	if err != nil {
		return r, err
	}
	count, err := g.git("rev-list", "--count", "HEAD")
	if err != nil {
		return r, err
	}
	if r.Commits, err = strconv.Atoi(count); err != nil {
		return r, err
	}
	if r.Before, err = g.historySize(); err != nil {
		return r, err
	}
	if r.Commits <= 1 {
		r.After = r.Before
		return r, nil
	}
	fresh, err := g.git("commit-tree", "HEAD^{tree}", "-m", "ccsync: compact history")
	if err != nil {
		return r, err
	}
	ref := "refs/heads/" + branch
	_, err = g.git("push", "--quiet", "--force-with-lease="+ref+":"+old, "origin", fresh+":"+ref)
	if err != nil {
		if strings.Contains(err.Error(), "stale info") || strings.Contains(err.Error(), "fetch first") ||
			strings.Contains(err.Error(), "non-fast-forward") {
			return r, fmt.Errorf("%w: %v", ErrRemoteMoved, err)
		}
		return r, err
	}
	if _, err := g.git("reset", "--hard", "--quiet", fresh); err != nil {
		return r, err
	}
	// The remote is done; shrinking this clone is a bonus.
	if _, err := g.git("reflog", "expire", "--expire=now", "--all"); err == nil {
		_, _ = g.git("gc", "--quiet", "--prune=now")
	}
	r.After, err = g.historySize()
	return r, err
}

// historySize is how many bytes the clone's git objects take.
func (g gitTransport) historySize() (int64, error) {
	out, err := g.git("count-objects", "-v")
	if err != nil {
		return 0, err
	}
	var kib int64
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, ": ")
		if k == "size" || k == "size-pack" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, err
			}
			kib += n
		}
	}
	return kib << 10, nil
}

// forget removes the local clone. It refuses a directory that isn't a
// ccsync clone, so a wrong path can't delete anything else.
func (g gitTransport) forget() error {
	if _, err := os.Stat(g.s.Dir); os.IsNotExist(err) {
		return nil
	}
	_, gitErr := os.Stat(g.s.path(".git"))
	_, markerErr := os.Stat(g.s.path(markerFile))
	_, headErr := g.git("rev-parse", "--verify", "--quiet", "HEAD")
	emptyRepo := headErr != nil // an init that never got its first push through
	if gitErr != nil || (markerErr != nil && !emptyRepo) {
		return fmt.Errorf("%s doesn't look like a ccsync store clone; not deleting it", g.s.Dir)
	}
	return os.RemoveAll(g.s.Dir)
}
