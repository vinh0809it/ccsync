package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vinh-vh/ccsync/internal/crypto"
	"github.com/vinh-vh/ccsync/internal/session"
)

// Session ids name directories in the store, which other machines write;
// anything that could poison a filepath.Join is rejected.
func checkID(id string) error {
	if !session.ValidID(id) {
		return fmt.Errorf("invalid session id %q", id)
	}
	return nil
}

// Put encrypts and stages one session into the clone.
func (s *Store) Put(key *crypto.Key, meta Meta, payload []byte) error {
	if err := checkID(meta.SessionID); err != nil {
		return err
	}
	dir := s.path(sessionsDir, meta.SessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	metaBlob, err := key.Encrypt(metaJSON)
	if err != nil {
		return err
	}
	if err := s.writeData(meta.SessionID, func(w io.Writer) error { return key.EncryptTo(w, payload) }); err != nil {
		return err
	}
	return os.WriteFile(s.path(sessionsDir, meta.SessionID, "meta.age"), metaBlob, 0o644)
}

// partSize bounds each file of a session's encrypted bundle. Git hosts
// refuse large files (GitHub: 100 MB hard limit, warning at 50 MB), so a
// big bundle is stored as data.age, data.age.1, data.age.2, ...
const partSize = 45 << 20

func partName(i int) string {
	if i == 0 {
		return "data.age"
	}
	return fmt.Sprintf("data.age.%d", i)
}

// writeData stores a session's encrypted bundle, streamed by write, in
// parts of at most partSize, removing stale parts of an earlier version.
func (s *Store) writeData(id string, write func(io.Writer) error) error {
	stale, err := filepath.Glob(s.path(sessionsDir, id, "data.age.*"))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	pw := &partWriter{dir: s.path(sessionsDir, id)}
	return errors.Join(write(pw), pw.Close())
}

// partWriter writes a stream as data.age, data.age.1, ... of at most
// partSize bytes each.
type partWriter struct {
	dir  string
	n    int      // index of the open part
	f    *os.File // open part, nil before the first write
	size int      // bytes in the open part
}

func (p *partWriter) Write(b []byte) (int, error) {
	written := 0
	for len(b) > 0 {
		if p.f == nil || p.size == partSize {
			if err := p.next(); err != nil {
				return written, err
			}
		}
		n, err := p.f.Write(b[:min(len(b), partSize-p.size)])
		written += n
		p.size += n
		if err != nil {
			return written, err
		}
		b = b[n:]
	}
	return written, nil
}

// next closes the open part and starts the following one.
func (p *partWriter) next() error {
	if p.f != nil {
		if err := p.f.Close(); err != nil {
			return err
		}
		p.n++
	}
	f, err := os.OpenFile(filepath.Join(p.dir, partName(p.n)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	p.f, p.size = f, 0
	return err
}

// Close finishes the last part (creating an empty data.age if nothing was
// written).
func (p *partWriter) Close() error {
	if p.f == nil {
		if err := p.next(); err != nil {
			return err
		}
	}
	return p.f.Close()
}

// Remove deletes one session from the clone; Commit stages the deletion.
// It reports whether the session was there.
func (s *Store) Remove(id string) (bool, error) {
	if err := checkID(id); err != nil {
		return false, err
	}
	dir := s.path(sessionsDir, id)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return false, nil
	}
	return true, os.RemoveAll(dir)
}

// List decrypts all session metadata in the store. Entries that can't be
// read (e.g. pushed by an outdated ccsync) are reported in unreadable
// instead of hiding the rest.
func (s *Store) List(key *crypto.Key) (metas []Meta, unreadable []string, err error) {
	ids, err := s.ids()
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		m, found, err := s.Lookup(key, id)
		switch {
		case err != nil:
			unreadable = append(unreadable, id)
		case found:
			metas = append(metas, m)
		}
	}
	return metas, unreadable, nil
}

// ids returns the session directory names that are safe to use.
func (s *Store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.path(sessionsDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && session.ValidID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// Lookup returns the stored metadata for an exact session id, or
// found=false.
func (s *Store) Lookup(key *crypto.Key, id string) (m Meta, found bool, err error) {
	if err := checkID(id); err != nil {
		return Meta{}, false, err
	}
	blob, err := os.ReadFile(s.path(sessionsDir, id, "meta.age"))
	if os.IsNotExist(err) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, false, err
	}
	plain, err := key.Decrypt(blob)
	if err != nil {
		return Meta{}, false, fmt.Errorf("session %s: %w", id, err)
	}
	if err := json.Unmarshal(plain, &m); err != nil {
		return Meta{}, false, fmt.Errorf("session %s: %w", id, err)
	}
	// The id inside the encrypted metadata must match its directory, so a
	// peer can't make one session pose as another.
	if m.SessionID != id {
		return Meta{}, false, fmt.Errorf("session %s: metadata id mismatch", id)
	}
	return m, true, nil
}

// Find resolves a (possibly abbreviated) session id to its metadata.
func (s *Store) Find(key *crypto.Key, idPrefix string) (Meta, error) {
	if idPrefix == "" {
		return Meta{}, fmt.Errorf("empty session id")
	}
	metas, _, err := s.List(key)
	if err != nil {
		return Meta{}, err
	}
	var match *Meta
	for i := range metas {
		if strings.HasPrefix(metas[i].SessionID, idPrefix) {
			if match != nil {
				return Meta{}, fmt.Errorf("session id %q is ambiguous", idPrefix)
			}
			match = &metas[i]
		}
	}
	if match == nil {
		return Meta{}, fmt.Errorf("no session in the store matches %q", idPrefix)
	}
	return *match, nil
}

// OpenPayload streams one session's decrypted bundle; the caller closes it.
func (s *Store) OpenPayload(key *crypto.Key, id string) (io.ReadCloser, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var parts []io.Reader
	var files []*os.File
	closeAll := func() error {
		var errs []error
		for _, f := range files {
			errs = append(errs, f.Close())
		}
		return errors.Join(errs...)
	}
	for i := 0; ; i++ {
		f, err := os.Open(s.path(sessionsDir, id, partName(i)))
		if i > 0 && os.IsNotExist(err) {
			break
		}
		if err != nil {
			closeAll()
			return nil, err
		}
		files = append(files, f)
		parts = append(parts, f)
	}
	r, err := key.DecryptFrom(io.MultiReader(parts...))
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("session %s: %w", id, err)
	}
	return readCloser{r, closeAll}, nil
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }
