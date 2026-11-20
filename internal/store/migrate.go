package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/vinh0809it/ccsync/internal/crypto"
	"github.com/vinh0809it/ccsync/internal/session"
)

// Migrate upgrades a format-1 store to the current format, and converts
// any session still encrypted the format-1 way (one pushed by an outdated
// ccsync after an earlier migration). It returns how many sessions were
// converted. Running it on an up-to-date store is a no-op.
func (s *Store) Migrate(passphrase string) (int, error) {
	if err := s.Sync(); err != nil {
		return 0, err
	}
	key, err := s.migrationKey(passphrase)
	if err != nil {
		return 0, err
	}
	ids, err := s.ids()
	if err != nil {
		return 0, err
	}
	converted := 0
	for _, id := range ids {
		if _, _, err := s.Lookup(key, id); err == nil {
			continue // already current
		}
		if err := s.convertLegacy(key, passphrase, id); err != nil {
			return 0, fmt.Errorf("session %s: %w", id, err)
		}
		converted++
	}
	if err := s.Commit(fmt.Sprintf("ccsync: migrate store to format %d", Format)); err != nil {
		if errors.Is(err, ErrNothingToCommit) {
			return 0, nil
		}
		return 0, err
	}
	return converted, s.Push()
}

// migrationKey returns the store key, creating it (and the format-2 key
// files) when the store is still at format 1.
func (s *Store) migrationKey(passphrase string) (*crypto.Key, error) {
	format, err := s.format()
	if err != nil {
		return nil, err
	}
	switch format {
	case 1:
		if err := s.verifyCheck(passphrase); err != nil {
			return nil, err
		}
		key, err := crypto.NewKey()
		if err != nil {
			return nil, err
		}
		return key, s.writeKeyFiles(passphrase, key)
	case Format:
		return s.Unlock(passphrase)
	default:
		return nil, fmt.Errorf("can't migrate a store with format %d", format)
	}
}

// convertLegacy re-encrypts one format-1 session (passphrase-encrypted raw
// transcript) as a current-format bundle.
func (s *Store) convertLegacy(key *crypto.Key, passphrase, id string) error {
	metaBlob, err1 := os.ReadFile(s.path(sessionsDir, id, "meta.age"))
	dataBlob, err2 := s.readPart(id, partName(0))
	if err := errors.Join(err1, err2); err != nil {
		return err
	}
	metaJSON, err := crypto.Decrypt(passphrase, metaBlob)
	if err != nil {
		return err
	}
	var m Meta
	if err := json.Unmarshal(metaJSON, &m); err != nil || m.SessionID != id {
		return fmt.Errorf("unreadable metadata")
	}
	transcript, err := crypto.Decrypt(passphrase, dataBlob)
	if err != nil {
		return err
	}
	payload, hash, err := session.Pack(session.Contents{Transcript: transcript})
	if err != nil {
		return err
	}
	m.ContentHash = hash
	return s.Put(key, m, payload)
}

// readPart reads one whole bundle file.
func (s *Store) readPart(id, name string) ([]byte, error) {
	f, err := s.t.openPart(id, name)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	return data, errors.Join(err, f.Close())
}
