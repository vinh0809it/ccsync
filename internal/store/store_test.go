package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinhvh0809/ccsync/internal/crypto"
	"github.com/vinhvh0809/ccsync/internal/testutil"
)

const pass = "test-passphrase"

// machine switches to a fresh simulated computer and opens its store.
func machine(t *testing.T) *Store {
	t.Helper()
	testutil.NewMachine(t)
	st, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// open inits st against remote and returns the unlocked key.
func open(t *testing.T, st *Store, remote string) *crypto.Key {
	t.Helper()
	if err := st.Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	key, err := st.Unlock(pass)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func put(t *testing.T, st *Store, key *crypto.Key, id string, payload string) {
	t.Helper()
	m := Meta{SessionID: id, Title: "t-" + id[:8], OriginalCwd: "/home/alice/webapp", Hostname: "machine-a", SessionMtime: time.Now()}
	if err := st.Put(key, m, []byte(payload)); err != nil {
		t.Fatal(err)
	}
}

// publish commits and pushes, failing the test on error.
func publish(t *testing.T, st *Store) {
	t.Helper()
	if err := st.Commit("test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Push(); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)
	if _, err := st.Unlock("wrong"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}

	id := "33333333-aaaa-bbbb-cccc-000000000001"
	put(t, st, key, id, "transcript API_KEY=hunter2")
	publish(t, st)
	if err := st.Commit("empty"); err != ErrNothingToCommit {
		t.Fatalf("commit with nothing staged: %v", err)
	}

	metas, unreadable, err := st.List(key)
	if err != nil || len(metas) != 1 || len(unreadable) != 0 || metas[0].SessionID != id {
		t.Fatalf("List = %+v %v %v", metas, unreadable, err)
	}
	m, err := st.Find(key, "33333333")
	if err != nil || m.SessionID != id {
		t.Fatalf("Find = %+v %v", m, err)
	}
	if data, err := st.Payload(key, id); err != nil || string(data) != "transcript API_KEY=hunter2" {
		t.Fatalf("Payload = %q %v", data, err)
	}
	if _, err := st.Find(key, "99999999"); err == nil {
		t.Fatal("unknown id should fail")
	}
}

// TestNothingReadableInRemote is the "public repo" guarantee: no plaintext
// from transcripts or metadata may appear anywhere in the pushed objects.
func TestNothingReadableInRemote(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)
	m := Meta{
		SessionID:    "44444444-aaaa-bbbb-cccc-000000000001",
		Title:        "MARKER_SECRET_TITLE",
		OriginalCwd:  "/home/alice/topsecret",
		Hostname:     "MARKER_HOSTNAME",
		SessionMtime: time.Now(),
	}
	if err := st.Put(key, m, []byte(`{"cwd":"/home/alice/topsecret","note":"MARKER_SECRET_CONTENT"}`)); err != nil {
		t.Fatal(err)
	}
	publish(t, st)

	// The working tree holds the uncompressed blobs; git objects in the
	// remote are compressed but checked too.
	for _, root := range []string{st.path(sessionsDir), remote} {
		for _, marker := range []string{"MARKER_SECRET_CONTENT", "MARKER_SECRET_TITLE", "MARKER_HOSTNAME", "topsecret", "alice"} {
			if grepTree(t, root, marker) {
				t.Fatalf("plaintext %q found under %s", marker, root)
			}
		}
	}
}

func grepTree(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return err
		}
		data, err := os.ReadFile(path)
		found = strings.Contains(string(data), needle)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// TestForgedMetaRejected plants metadata whose session_id doesn't match its
// directory (the path-traversal vector) and checks it never surfaces.
func TestForgedMetaRejected(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)
	evil := st.path(sessionsDir, "66666666-aaaa-bbbb-cccc-00000000000e")
	os.MkdirAll(evil, 0o755)
	for name, plain := range map[string]string{
		"meta.age": `{"session_id":"../../../../home/victim/.bashrc","title":"forged"}`,
		"data.age": "payload",
	} {
		blob, _ := key.Encrypt([]byte(plain))
		os.WriteFile(filepath.Join(evil, name), blob, 0o644)
	}

	metas, unreadable, err := st.List(key)
	if err != nil || len(metas) != 0 || len(unreadable) != 1 {
		t.Fatalf("forged entry must be reported unreadable: %+v %v %v", metas, unreadable, err)
	}
	if _, err := st.Find(key, "66666666"); err == nil {
		t.Fatal("Find returned a session with mismatched metadata id")
	}
}

func TestInitRejectsChangedRemote(t *testing.T) {
	st := machine(t)
	open(t, st, testutil.Remote(t))
	err := st.Init(testutil.Remote(t), pass)
	if err == nil || !strings.Contains(err.Error(), "tracks") {
		t.Fatalf("re-init with a different remote must fail, got: %v", err)
	}
}

func TestTwoMachines(t *testing.T) {
	remote := testutil.Remote(t)
	a := machine(t)
	keyA := open(t, a, remote)
	put(t, a, keyA, "55555555-aaaa-bbbb-cccc-000000000001", "from A")
	publish(t, a)

	b := machine(t)
	if err := b.Init(remote, "wrong-pass"); err == nil {
		t.Fatal("init with a wrong passphrase should fail")
	}
	keyB := open(t, b, remote)
	if data, err := b.Payload(keyB, "55555555-aaaa-bbbb-cccc-000000000001"); err != nil || string(data) != "from A" {
		t.Fatalf("machine B read %q, %v", data, err)
	}
}

// TestRemove deletes a session and publishes the deletion.
func TestRemove(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)
	id := "56565656-aaaa-bbbb-cccc-000000000001"
	put(t, st, key, id, "x")
	publish(t, st)

	if found, err := st.Remove(id); err != nil || !found {
		t.Fatalf("Remove = %v %v", found, err)
	}
	if found, _ := st.Remove(id); found {
		t.Fatal("second Remove found the session again")
	}
	if _, err := st.Remove("../escape"); err == nil {
		t.Fatal("Remove accepted an unsafe id")
	}
	publish(t, st)
	if strings.Contains(testutil.RemoteFiles(t, remote), id) {
		t.Fatal("session still in the remote")
	}
}

// TestSyncRecoversDirtyClone: leftovers of an interrupted run (staged,
// modified, untracked or committed-but-unpushed files) are discarded.
func TestSyncRecoversDirtyClone(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)

	put(t, st, key, "77777777-aaaa-bbbb-cccc-000000000001", "unpushed")
	if err := st.Commit("never pushed"); err != nil {
		t.Fatal(err)
	}
	put(t, st, key, "77777777-aaaa-bbbb-cccc-000000000002", "staged")
	testutil.Git(t, st.Dir, "add", "-A")
	os.WriteFile(st.path("stray.txt"), []byte("junk"), 0o644)
	os.WriteFile(st.path(markerFile), []byte("garbage"), 0o644)

	if err := st.Sync(); err != nil {
		t.Fatal(err)
	}
	if out := testutil.Git(t, st.Dir, "status", "--porcelain"); out != "" {
		t.Fatalf("clone still dirty after Sync: %q", out)
	}
	if ahead := testutil.Git(t, st.Dir, "rev-list", "--count", "@{u}..HEAD"); ahead != "0" {
		t.Fatalf("unpushed commits survived Sync: %s", ahead)
	}
	if _, err := st.Unlock(pass); err != nil {
		t.Fatalf("store unusable after recovery: %v", err)
	}
}

// TestSyncRefusesNonStore: Sync resets hard, so it must never run in a
// clone whose remote isn't a ccsync store.
func TestSyncRefusesNonStore(t *testing.T) {
	remote := testutil.Remote(t)
	work := t.TempDir()
	testutil.Git(t, "", "clone", "--quiet", remote, work)
	os.WriteFile(filepath.Join(work, "precious.txt"), []byte("data"), 0o644)
	testutil.Git(t, work, "add", "-A")
	testutil.Git(t, work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x")
	testutil.Git(t, work, "push", "-q", "-u", "origin", "HEAD")
	os.WriteFile(filepath.Join(work, "uncommitted.txt"), []byte("keep me"), 0o644)

	st := &Store{Dir: work}
	if err := st.Sync(); err == nil {
		t.Fatal("Sync ran in a non-ccsync repo")
	}
	if _, err := os.Stat(filepath.Join(work, "uncommitted.txt")); err != nil {
		t.Fatal("Sync destroyed files in a non-ccsync repo")
	}
}

// TestPushDetectsRemoteMoved: a push from a clone that fell behind fails
// with ErrRemoteMoved (which callers retry) instead of a generic error.
func TestPushDetectsRemoteMoved(t *testing.T) {
	remote := testutil.Remote(t)
	a := machine(t)
	keyA := open(t, a, remote)
	b := machine(t)
	keyB := open(t, b, remote)

	put(t, b, keyB, "88888888-aaaa-bbbb-cccc-000000000001", "from B")
	publish(t, b)

	put(t, a, keyA, "88888888-aaaa-bbbb-cccc-000000000002", "from A")
	if err := a.Commit("A"); err != nil {
		t.Fatal(err)
	}
	if err := a.Push(); !errors.Is(err, ErrRemoteMoved) {
		t.Fatalf("want ErrRemoteMoved, got %v", err)
	}
}

// TestInitRecoversFromFailedFirstPush: when the very first push is
// rejected (typically bad credentials), init must fail, and a re-run must
// create the store instead of reporting success with an empty remote.
func TestInitRecoversFromFailedFirstPush(t *testing.T) {
	remote := testutil.Remote(t)
	testutil.RejectFirstPush(t, remote)
	st := machine(t)
	if err := st.Init(remote, pass); err == nil {
		t.Fatal("init must fail when its push is rejected")
	}
	key := open(t, st, remote)
	put(t, st, key, "abababab-aaaa-bbbb-cccc-000000000001", "x")
	publish(t, st)
}

// legacyStore pushes a format-1 store (as ccsync 0.1.x wrote it) holding
// one session.
func legacyStore(t *testing.T, remote, id, transcript string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "legacy")
	testutil.Git(t, "", "clone", "--quiet", remote, dir)
	check, _ := crypto.NewCheckBlob(pass)
	meta, _ := crypto.Encrypt(pass, []byte(`{"session_id":"`+id+`","title":"old","original_cwd":"/home/alice/webapp"}`))
	data, _ := crypto.Encrypt(pass, []byte(transcript))
	files := map[string][]byte{
		markerFile: []byte(`{"format":1}` + "\n"),
		checkFile:  check,
		filepath.Join(sessionsDir, id, "meta.age"): meta,
		filepath.Join(sessionsDir, id, "data.age"): data,
	}
	for name, content := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), content, 0o644)
	}
	testutil.Git(t, dir, "add", "-A")
	testutil.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "v1")
	testutil.Git(t, dir, "push", "-q", "-u", "origin", "HEAD")
}

func TestMigrateFromFormat1(t *testing.T) {
	remote := testutil.Remote(t)
	id := "99999999-aaaa-bbbb-cccc-000000000001"
	legacyStore(t, remote, id, "raw transcript")

	// A new machine can join a format-1 store, but must migrate to use it.
	st := machine(t)
	if err := st.Init(remote, pass); err != nil {
		t.Fatalf("init on a format-1 store: %v", err)
	}
	if _, err := st.Unlock(pass); err != ErrNeedsMigration {
		t.Fatalf("want ErrNeedsMigration, got %v", err)
	}
	wrap := func(b []byte) ([]byte, string, error) { return append([]byte("bundle:"), b...), "h", nil }
	if _, err := st.Migrate("wrong", wrap); err == nil {
		t.Fatal("migrate accepted a wrong passphrase")
	}
	if n, err := st.Migrate(pass, wrap); err != nil || n != 1 {
		t.Fatalf("migrate: n=%d err=%v", n, err)
	}
	if n, err := st.Migrate(pass, wrap); err != nil || n != 0 {
		t.Fatalf("second migrate should be a no-op: n=%d err=%v", n, err)
	}

	other := machine(t)
	key := open(t, other, remote)
	m, err := other.Find(key, "99999999")
	if err != nil || m.Title != "old" || m.ContentHash != "h" {
		t.Fatalf("migrated meta: %+v %v", m, err)
	}
	if data, _ := other.Payload(key, id); string(data) != "bundle:raw transcript" {
		t.Fatalf("migrated payload: %q", data)
	}
}
