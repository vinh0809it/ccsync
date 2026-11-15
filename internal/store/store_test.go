package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinhvh0809/ccsync/internal/crypto"
)

// open inits this machine against remote and returns the unlocked key.
func open(t *testing.T, remote string) *crypto.Key {
	t.Helper()
	if err := Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	key, err := Unlock(pass)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func put(t *testing.T, key *crypto.Key, id, title string, payload []byte) {
	t.Helper()
	if err := Put(key, Meta{SessionID: id, Title: title, OriginalCwd: "/home/alice/webapp", Hostname: "machine-a", SessionMtime: time.Now()}, payload); err != nil {
		t.Fatal(err)
	}
}

// newRemote creates a bare git repo to act as the store's origin.
func newRemote(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return "file://" + remote
}

// machine points config+store env at fresh dirs, simulating one computer.
func machine(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CCSYNC_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("CCSYNC_STORE_DIR", filepath.Join(base, "store"))
}

const pass = "test-passphrase"

func TestInitPushListGetRoundTrip(t *testing.T) {
	remote := newRemote(t)
	machine(t)

	key := open(t, remote)
	if _, err := Unlock("wrong"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}

	transcript := []byte(`{"cwd":"/home/alice/webapp","secret":"API_KEY=hunter2"}` + "\n")
	meta := Meta{
		SessionID:    "33333333-aaaa-bbbb-cccc-000000000001",
		Title:        "secret-work",
		ProjectLabel: "webapp",
		OriginalCwd:  "/home/alice/webapp",
		Hostname:     "machine-a",
		SessionMtime: time.Now(),
		Size:         int64(len(transcript)),
		PushedAt:     time.Now().UTC(),
	}
	if err := Put(key, meta, transcript); err != nil {
		t.Fatal(err)
	}
	if err := Commit("test push"); err != nil {
		t.Fatal(err)
	}
	if err := Push(); err != nil {
		t.Fatal(err)
	}
	// Nothing staged now: Commit must refuse.
	if err := Commit("empty"); err != ErrNothingToCommit {
		t.Fatalf("commit with nothing staged: %v", err)
	}

	metas, _, err := List(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Title != "secret-work" || metas[0].OriginalCwd != "/home/alice/webapp" {
		t.Fatalf("bad listing: %+v", metas)
	}

	got, data, err := Get(key, "33333333")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != meta.SessionID || string(data) != string(transcript) {
		t.Fatal("Get returned wrong session or corrupted data")
	}
	if _, _, err := Get(key, "99999999"); err == nil {
		t.Fatal("unknown id should fail")
	}
}

// TestNothingReadableInRemote is the "public repo" guarantee: no plaintext
// from transcripts or metadata may appear anywhere in the pushed objects.
func TestNothingReadableInRemote(t *testing.T) {
	remote := newRemote(t)
	machine(t)
	key := open(t, remote)
	transcript := []byte(`{"cwd":"/home/alice/topsecret","note":"MARKER_SECRET_CONTENT"}` + "\n")
	meta := Meta{
		SessionID:    "44444444-aaaa-bbbb-cccc-000000000001",
		Title:        "MARKER_SECRET_TITLE",
		ProjectLabel: "topsecret",
		OriginalCwd:  "/home/alice/topsecret",
		Hostname:     "MARKER_HOSTNAME",
		SessionMtime: time.Now(),
	}
	if err := Put(key, meta, transcript); err != nil {
		t.Fatal(err)
	}
	if err := Commit("push"); err != nil {
		t.Fatal(err)
	}
	if err := Push(); err != nil {
		t.Fatal(err)
	}

	// Check the store's working tree (uncompressed .age files) AND the bare
	// remote. Git objects are zlib-compressed, so the working tree is the
	// reliable place to catch unencrypted content.
	storeDir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	remotePath := strings.TrimPrefix(remote, "file://")
	for _, root := range []string{filepath.Join(storeDir, "sessions"), remotePath} {
		for _, marker := range []string{"MARKER_SECRET_CONTENT", "MARKER_SECRET_TITLE", "MARKER_HOSTNAME", "topsecret", "alice"} {
			if grepTree(t, root, marker) {
				t.Fatalf("plaintext %q found under %s", marker, root)
			}
		}
	}
}

// grepTree reports whether needle appears in any file under root
// (including inside git pack/object files).
func grepTree(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// TestForgedMetaRejected plants a peer-crafted meta.age whose session_id
// doesn't match its directory (the path-traversal vector) and checks that
// List/Get refuse to surface it.
func TestForgedMetaRejected(t *testing.T) {
	remote := newRemote(t)
	machine(t)
	key := open(t, remote)
	dir, _ := Dir()
	evil := filepath.Join(dir, "sessions", "66666666-aaaa-bbbb-cccc-00000000000e")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	metaJSON := `{"session_id":"../../../../home/victim/.bashrc","title":"forged"}`
	blob, err := key.Encrypt([]byte(metaJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "meta.age"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := key.Encrypt([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "data.age"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	metas, _, err := List(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if m.Title == "forged" {
			t.Fatal("forged meta surfaced in List")
		}
	}
	if _, _, err := Get(key, "66666666"); err == nil {
		t.Fatal("Get returned a session with mismatched metadata id")
	}
	// A directory name that is itself unsafe is skipped outright.
	if !ValidSessionID("11111111-aaaa-bbbb-cccc-000000000001") || ValidSessionID("../x") || ValidSessionID("a/b") {
		t.Fatal("ValidSessionID misclassifies")
	}
}

// TestInitRejectsChangedRemote ensures re-init with a different URL fails
// loudly instead of silently syncing against the old remote.
func TestInitRejectsChangedRemote(t *testing.T) {
	machine(t)
	if err := Init(newRemote(t), pass); err != nil {
		t.Fatal(err)
	}
	err := Init(newRemote(t), pass)
	if err == nil || !strings.Contains(err.Error(), "tracks") {
		t.Fatalf("re-init with a different remote must fail, got: %v", err)
	}
}

// TestTwoMachines pushes from machine A and reads everything back on a
// freshly initialized machine B via the shared remote.
func TestTwoMachines(t *testing.T) {
	remote := newRemote(t)

	// Machine A
	machine(t)
	key := open(t, remote)
	transcript := []byte(`{"cwd":"/home/alice/webapp"}` + "\n")
	meta := Meta{
		SessionID:    "55555555-aaaa-bbbb-cccc-000000000001",
		Title:        "cross-machine",
		OriginalCwd:  "/home/alice/webapp",
		ProjectLabel: "webapp",
		Hostname:     "machine-a",
		SessionMtime: time.Now(),
	}
	if err := Put(key, meta, transcript); err != nil {
		t.Fatal(err)
	}
	if err := Commit("from A"); err != nil {
		t.Fatal(err)
	}
	if err := Push(); err != nil {
		t.Fatal(err)
	}

	// Machine B: fresh config/store dirs, same remote + passphrase.
	machine(t)
	if err := Init(remote, "wrong-pass"); err == nil {
		t.Fatal("machine B init with wrong passphrase should fail")
	}
	keyB := open(t, remote)
	got, data, err := Get(keyB, "55555555")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "machine-a" || string(data) != string(transcript) {
		t.Fatal("machine B did not receive machine A's session intact")
	}
}

// TestSyncRecoversDirtyClone: leftovers of an interrupted run (staged,
// modified, untracked or committed-but-unpushed files) are discarded.
func TestSyncRecoversDirtyClone(t *testing.T) {
	remote := newRemote(t)
	machine(t)
	key := open(t, remote)
	dir, _ := Dir()

	put(t, key, "77777777-aaaa-bbbb-cccc-000000000001", "unpushed", []byte("x"))
	if err := Commit("never pushed"); err != nil {
		t.Fatal(err)
	}
	put(t, key, "77777777-aaaa-bbbb-cccc-000000000002", "staged", []byte("y"))
	git(dir, "add", "-A")
	os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("junk"), 0o644)
	os.WriteFile(filepath.Join(dir, markerFile), []byte("garbage"), 0o644)

	if err := Sync(); err != nil {
		t.Fatal(err)
	}
	if out, _ := git(dir, "status", "--porcelain"); out != "" {
		t.Fatalf("clone still dirty after Sync: %q", out)
	}
	if ahead, _ := git(dir, "rev-list", "--count", "@{u}..HEAD"); ahead != "0" {
		t.Fatalf("unpushed commits survived Sync: %s", ahead)
	}
	if _, err := Unlock(pass); err != nil {
		t.Fatalf("store unusable after recovery: %v", err)
	}
}

// TestPushDetectsRemoteMoved: a push from a clone that fell behind fails
// with ErrRemoteMoved (which callers retry) instead of a generic error.
func TestPushDetectsRemoteMoved(t *testing.T) {
	remote := newRemote(t)
	machine(t)
	key := open(t, remote)
	cfgA, storeA := os.Getenv("CCSYNC_CONFIG_DIR"), os.Getenv("CCSYNC_STORE_DIR")

	machine(t)
	keyB := open(t, remote)
	put(t, keyB, "88888888-aaaa-bbbb-cccc-000000000001", "from B", []byte("b"))
	if err := Commit("B"); err != nil {
		t.Fatal(err)
	}
	if err := Push(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CCSYNC_CONFIG_DIR", cfgA)
	t.Setenv("CCSYNC_STORE_DIR", storeA)
	put(t, key, "88888888-aaaa-bbbb-cccc-000000000002", "from A", []byte("a"))
	if err := Commit("A"); err != nil {
		t.Fatal(err)
	}
	if err := Push(); !errors.Is(err, ErrRemoteMoved) {
		t.Fatalf("want ErrRemoteMoved, got %v", err)
	}
}

// legacyStore writes a format-1 store (as ccsync 0.1.x did) holding one
// session, and pushes it.
func legacyStore(t *testing.T, remote, id string, transcript []byte) {
	t.Helper()
	machine(t)
	dir, _ := Dir()
	if out, err := exec.Command("git", "clone", "--quiet", remote, dir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	check, _ := crypto.NewCheckBlob(pass)
	os.WriteFile(filepath.Join(dir, markerFile), []byte(`{"format":1}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, checkFile), check, 0o644)
	sdir := filepath.Join(dir, "sessions", id)
	os.MkdirAll(sdir, 0o755)
	meta, _ := crypto.Encrypt(pass, []byte(`{"session_id":"`+id+`","title":"old","original_cwd":"/home/alice/webapp"}`))
	data, _ := crypto.Encrypt(pass, transcript)
	os.WriteFile(filepath.Join(sdir, "meta.age"), meta, 0o644)
	os.WriteFile(filepath.Join(sdir, "data.age"), data, 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "v1"}, {"push", "-q", "-u", "origin", "HEAD"}} {
		if _, err := git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateFromFormat1(t *testing.T) {
	remote := newRemote(t)
	id := "99999999-aaaa-bbbb-cccc-000000000001"
	transcript := []byte(`{"cwd":"/home/alice/webapp"}` + "\n")
	legacyStore(t, remote, id, transcript)

	// A new machine can join a format-1 store, but must migrate to use it.
	machine(t)
	if err := Init(remote, pass); err != nil {
		t.Fatalf("init on a format-1 store: %v", err)
	}
	if _, err := Unlock(pass); err != ErrNeedsMigration {
		t.Fatalf("want ErrNeedsMigration, got %v", err)
	}
	if _, err := Migrate("wrong", nil); err == nil {
		t.Fatal("migrate accepted a wrong passphrase")
	}
	wrap := func(b []byte) ([]byte, string, error) { return append([]byte("bundle:"), b...), "h", nil }
	if n, err := Migrate(pass, wrap); err != nil || n != 1 {
		t.Fatalf("migrate: n=%d err=%v", n, err)
	}
	if n, err := Migrate(pass, wrap); err != nil || n != 0 {
		t.Fatalf("second migrate should be a no-op: n=%d err=%v", n, err)
	}

	// Another machine sees the migrated store.
	machine(t)
	key := open(t, remote)
	m, payload, err := Get(key, "99999999")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "old" || m.ContentHash != "h" || string(payload) != "bundle:"+string(transcript) {
		t.Fatalf("migrated session wrong: %+v %q", m, payload)
	}
}

// TestInitRecoversFromFailedFirstPush: when the very first push is
// rejected (typically bad credentials), init must fail, and a re-run must
// create the store properly instead of reporting success with nothing on
// the remote.
func TestInitRecoversFromFailedFirstPush(t *testing.T) {
	remote := newRemote(t)
	bare := strings.TrimPrefix(remote, "file://")
	marker := filepath.Join(bare, "rejected-once")
	hook := "#!/bin/sh\n[ -e '" + marker + "' ] && exit 0\ntouch '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	machine(t)
	if err := Init(remote, pass); err == nil {
		t.Fatal("init must fail when its push is rejected")
	}
	key := open(t, remote)
	if err := Sync(); err != nil {
		t.Fatalf("store unusable after a failed first init: %v", err)
	}
	put(t, key, "abababab-aaaa-bbbb-cccc-000000000001", "x", []byte("x"))
	if err := Commit("x"); err != nil {
		t.Fatal(err)
	}
	if err := Push(); err != nil {
		t.Fatal(err)
	}
}

// TestSyncRefusesNonStore: Sync resets hard, so it must never run in a
// directory whose remote isn't a ccsync store.
func TestSyncRefusesNonStore(t *testing.T) {
	remote := newRemote(t)
	bare := strings.TrimPrefix(remote, "file://")
	work := t.TempDir()
	for _, args := range [][]string{{"clone", "-q", bare, work}} {
		exec.Command("git", args...).Run()
	}
	os.WriteFile(filepath.Join(work, "precious.txt"), []byte("data"), 0o644)
	git(work, "add", "-A")
	git(work, "commit", "-qm", "x")
	git(work, "push", "-q", "-u", "origin", "HEAD")
	os.WriteFile(filepath.Join(work, "uncommitted.txt"), []byte("keep me"), 0o644)
	t.Setenv("CCSYNC_STORE_DIR", work)
	if err := Sync(); err == nil {
		t.Fatal("Sync ran in a non-ccsync repo")
	}
	if _, err := os.Stat(filepath.Join(work, "uncommitted.txt")); err != nil {
		t.Fatal("Sync destroyed files in a non-ccsync repo")
	}
}
