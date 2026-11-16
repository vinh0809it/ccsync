package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vinh0809it/ccsync/internal/testutil"
)

// TestFolderStore: two machines share a store through a folder that a sync
// service would keep in step (here: the same directory).
func TestFolderStore(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "Dropbox", "claude-sessions")
	testutil.NewMachine(t)
	a := Open(Entry{Name: "f", Kind: KindFolder, Dir: shared})
	if err := a.Init(shared, pass); err != nil {
		t.Fatal(err)
	}
	keyA, _ := a.Unlock(pass)
	put(t, a, keyA, "a1a1a1a1-aaaa-bbbb-cccc-000000000001", "hello")
	if err := a.Commit("x"); err != nil {
		t.Fatal(err)
	}
	if err := a.Push(); err != nil {
		t.Fatal(err)
	}
	// No temp files left for the sync service to ship.
	if left, _ := filepath.Glob(filepath.Join(shared, "*", "*", tempPrefix+"*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}

	testutil.NewMachine(t)
	b := Open(Entry{Name: "f", Kind: KindFolder, Dir: shared})
	if err := b.Init(shared, "wrong"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	if err := b.Init(shared, pass); err != nil {
		t.Fatal(err)
	}
	keyB, _ := b.Unlock(pass)
	if err := b.Sync(); err != nil {
		t.Fatal(err)
	}
	if data, err := payload(t, b, keyB, "a1a1a1a1-aaaa-bbbb-cccc-000000000001"); err != nil || string(data) != "hello" {
		t.Fatalf("B read %q, %v", data, err)
	}

	// Forgetting a folder store never deletes the shared folder.
	if err := b.Forget(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, markerFile)); err != nil {
		t.Fatal("forget removed the shared folder")
	}
}

// TestFolderStoreRefusesUnrelatedFolder: never write a store into a
// folder holding other things.
func TestFolderStoreRefusesUnrelatedFolder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "thesis.docx"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644)
	st := Open(Entry{Kind: KindFolder, Dir: dir})
	if err := st.Init(dir, pass); err == nil {
		t.Fatal("store created in a non-empty folder")
	}
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, ".DS_Store"), []byte("x"), 0o644)
	if err := Open(Entry{Kind: KindFolder, Dir: empty}).Init(empty, pass); err != nil {
		t.Fatalf("a folder with only dotfiles should do: %v", err)
	}
}
