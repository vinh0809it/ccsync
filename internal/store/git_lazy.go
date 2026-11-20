package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// A git store's clone checks out everything but the session bundles
// (sessions/<id>/data.age*): listing the store, telling sync status and
// previewing need only the metadata. A bundle is read from git when a
// session is pulled, downloading it then (the clone is partial).
//
// Writes keep that shape: Put writes a bundle into the working tree, and
// commit stages it straight into the index and removes the file again.

// sparsePatterns check out everything but the bundles (non-cone mode).
var sparsePatterns = []string{"/*", "!/" + sessionsDir + "/*/data.age*"}

// lazy makes the clone check out only what sparsePatterns allow. It is a
// no-op once set; an older clone with every bundle checked out drops them
// from its working tree (they stay in git).
//
// A clone made by an older ccsync is full; it also becomes partial from
// here on: later fetches skip blobs, and bundles come when read.
func (g gitTransport) lazy() error {
	if p, _ := g.git("config", "--get", "remote.origin.promisor"); p != "true" {
		for _, kv := range [][2]string{{"remote.origin.promisor", "true"}, {"remote.origin.partialclonefilter", "blob:none"}} {
			if _, err := g.git("config", kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	if on, _ := g.git("config", "--get", "core.sparseCheckout"); on == "true" {
		return nil
	}
	_, err := g.git(append([]string{"sparse-checkout", "set", "--no-cone"}, sparsePatterns...)...)
	return err
}

// dropStrayParts removes bundle files from the working tree. Bundles live
// there only between Put and commit; one left by a failed run must never
// be committed later.
func (g gitTransport) dropStrayParts() error {
	stray, err := filepath.Glob(g.s.path(sessionsDir, "*", "data.age*"))
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range stray {
		errs = append(errs, os.Remove(f))
	}
	return errors.Join(errs...)
}

// isPart reports whether a file name is a bundle part.
func isPart(name string) bool { return name == "data.age" || strings.HasPrefix(name, "data.age.") }

// indexParts maps each session id to the bundle parts the index holds.
func (g gitTransport) indexParts(prefix string) (map[string][]string, error) {
	out, err := g.git("ls-files", "-z", "--", prefix)
	if err != nil {
		return nil, err
	}
	parts := map[string][]string{}
	for _, f := range strings.Split(out, "\x00") {
		dir, name := path.Split(f)
		id := path.Base(dir)
		if f != "" && isPart(name) && path.Dir(path.Clean(dir)) == sessionsDir {
			parts[id] = append(parts[id], name)
		}
	}
	return parts, nil
}

// parts lists a session's bundle files: those written here and not yet
// committed, else those in git.
func (g gitTransport) parts(id string) ([]string, error) {
	if names, err := diskParts(g.s, id); err != nil || len(names) > 0 {
		return names, err
	}
	indexed, err := g.indexParts(sessionsDir + "/" + id + "/")
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range indexed[id] {
		have[n] = true
	}
	var names []string
	for i := 0; have[partName(i)]; i++ {
		names = append(names, partName(i))
	}
	return names, nil
}

// openPart opens a bundle file written here, or streams it from git,
// which downloads it first if the clone doesn't have it yet.
func (g gitTransport) openPart(id, name string) (io.ReadCloser, error) {
	if f, err := os.Open(g.s.path(sessionsDir, id, name)); err == nil || !os.IsNotExist(err) {
		return f, err
	}
	cmd := g.command(g.s.Dir, "cat-file", "blob", ":"+sessionsDir+"/"+id+"/"+name)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &gitBlob{ReadCloser: out, cmd: cmd, stderr: &stderr}, nil
}

// prefetch downloads the bundle blobs of ids that the clone lacks, in one
// fetch (reading them one by one costs a round trip each).
func (g gitTransport) prefetch(ids []string) error {
	var paths []string
	for _, id := range ids {
		paths = append(paths, sessionsDir+"/"+id+"/")
	}
	if len(paths) == 0 {
		return nil
	}
	// What the current tree lacks (--no-walk: not the history), listed
	// without fetching it...
	out, err := g.git("rev-list", "--objects", "--missing=print", "--no-object-names", "--no-walk", "HEAD")
	if err != nil {
		return err
	}
	absent := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if oid, ok := strings.CutPrefix(line, "?"); ok {
			absent[oid] = true
		}
	}
	// ...narrowed to these sessions' bundles.
	staged, err := g.git(append([]string{"ls-files", "-s", "--"}, paths...)...)
	if err != nil {
		return err
	}
	var missing []string
	for _, line := range strings.Split(staged, "\n") {
		fields := strings.Fields(line) // <mode> <oid> <stage> <path>
		if len(fields) == 4 && absent[fields[1]] && isPart(path.Base(fields[3])) {
			missing = append(missing, fields[1])
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// The fetch git itself makes for missing objects of a partial clone.
	_, err = g.git(append([]string{"-c", "fetch.negotiationAlgorithm=noop", "fetch", "--quiet", "--no-tags",
		"--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none", "origin"}, missing...)...)
	return err
}

// gitBlob is a blob streamed out of git; Close reports git's failure.
type gitBlob struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *strings.Builder
}

func (b *gitBlob) Close() error {
	_, _ = io.Copy(io.Discard, b.ReadCloser) // let git finish writing
	if err := b.cmd.Wait(); err != nil {
		return fmt.Errorf("reading from the store: %s", strings.TrimSpace(b.stderr.String()))
	}
	return nil
}

// stageParts brings the index's bundle files in line with the working
// tree, which holds only the bundles written since the last sync:
//   - a session written here gets exactly its new parts (stale ones go);
//   - a session whose metadata is gone (removed) loses its parts too;
//   - every other session keeps the parts it has in git.
//
// Staged bundles leave the working tree: the commit carries them.
func (g gitTransport) stageParts() error {
	indexed, err := g.indexParts(sessionsDir + "/")
	if err != nil {
		return err
	}
	written, err := filepath.Glob(g.s.path(sessionsDir, "*", "data.age*"))
	if err != nil {
		return err
	}
	rewritten := map[string]bool{}
	var add []string
	for _, f := range written {
		if strings.HasPrefix(filepath.Base(f), tempPrefix) || !isPart(filepath.Base(f)) {
			continue
		}
		rewritten[filepath.Base(filepath.Dir(f))] = true
		add = append(add, f)
	}
	var drop []string
	for id, names := range indexed {
		_, metaErr := os.Stat(g.s.path(sessionsDir, id, "meta.age"))
		for _, n := range names {
			_, statErr := os.Stat(g.s.path(sessionsDir, id, n))
			if os.IsNotExist(metaErr) || (rewritten[id] && os.IsNotExist(statErr)) {
				drop = append(drop, sessionsDir+"/"+id+"/"+n)
			}
		}
	}
	if len(drop) > 0 {
		if _, err := g.git(append([]string{"update-index", "--force-remove", "--"}, drop...)...); err != nil {
			return err
		}
	}
	var staged []string
	for _, f := range add {
		sha, err := g.git("hash-object", "-w", "--", f)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(g.s.Dir, f)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := g.git("update-index", "--add", "--cacheinfo", "100644,"+sha+","+rel); err != nil {
			return err
		}
		staged = append(staged, rel)
	}
	if len(staged) > 0 {
		// Outside the checkout like every other bundle: git must not see
		// the missing file as a deletion.
		if _, err := g.git(append([]string{"update-index", "--skip-worktree", "--"}, staged...)...); err != nil {
			return err
		}
	}
	var errs []error
	for _, f := range add {
		errs = append(errs, os.Remove(f))
	}
	return errors.Join(errs...)
}
