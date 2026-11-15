package store

import (
	"encoding/json"
	"fmt"
	"os"
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
	dataBlob, err := key.Encrypt(payload)
	if err != nil {
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
	if err := os.WriteFile(s.path(sessionsDir, meta.SessionID, "data.age"), dataBlob, 0o644); err != nil {
		return err
	}
	return os.WriteFile(s.path(sessionsDir, meta.SessionID, "meta.age"), metaBlob, 0o644)
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

// Payload decrypts one session's bundle.
func (s *Store) Payload(key *crypto.Key, id string) ([]byte, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	blob, err := os.ReadFile(s.path(sessionsDir, id, "data.age"))
	if err != nil {
		return nil, err
	}
	return key.Decrypt(blob)
}
