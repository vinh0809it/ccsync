package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinh0809it/ccsync/internal/crypto"
	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/testutil"
)

const pass = "test-passphrase"

// machine switches to a fresh simulated computer and opens its store.
func machine(t *testing.T) *Store {
	t.Helper()
	m := testutil.NewMachine(t)
	return Open(Entry{Name: "test", Dir: filepath.Join(m.Data, "stores", "test")})
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

// payload reads a session's decrypted bundle.
func payload(t *testing.T, st *Store, key *crypto.Key, id string) ([]byte, error) {
	t.Helper()
	r, err := st.OpenPayload(key, id)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
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
	if data, err := payload(t, st, key, id); err != nil || string(data) != "transcript API_KEY=hunter2" {
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
	for _, root := range []string{st.path(sessionsDir), testutil.RemoteDir(remote)} {
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
	if data, err := payload(t, b, keyB, "55555555-aaaa-bbbb-cccc-000000000001"); err != nil || string(data) != "from A" {
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
	testutil.Git(t, st.Dir, "add", "-A", "--sparse") // bundles are outside the checkout
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

	st := Open(Entry{Dir: work})
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
// one session, or none when id is empty.
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
	}
	if id != "" {
		files[filepath.Join(sessionsDir, id, "meta.age")] = meta
		files[filepath.Join(sessionsDir, id, "data.age")] = data
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
	if _, err := st.Migrate("wrong"); err == nil {
		t.Fatal("migrate accepted a wrong passphrase")
	}
	if n, err := st.Migrate(pass); err != nil || n != 1 {
		t.Fatalf("migrate: n=%d err=%v", n, err)
	}
	if n, err := st.Migrate(pass); err != nil || n != 0 {
		t.Fatalf("second migrate should be a no-op: n=%d err=%v", n, err)
	}

	other := machine(t)
	key := open(t, other, remote)
	m, err := other.Find(key, "99999999")
	if err != nil || m.Title != "old" || m.ContentHash == "" {
		t.Fatalf("migrated meta: %+v %v", m, err)
	}
	data, _ := payload(t, other, key, id)
	c, err := session.Unpack(bytes.NewReader(data))
	if err != nil || string(c.Transcript) != "raw transcript" {
		t.Fatalf("migrated bundle: %+v %v", c, err)
	}
}

// TestInitAdoptsRepoWithReadme: hosting services offer to create a repo
// with a README; init must build the store on top of it.
func TestInitAdoptsRepoWithReadme(t *testing.T) {
	remote := testutil.Remote(t)
	seed := filepath.Join(t.TempDir(), "seed")
	testutil.Git(t, "", "clone", "--quiet", remote, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("# my sessions\n"), 0o644)
	testutil.Git(t, seed, "add", "-A")
	testutil.Git(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "readme")
	testutil.Git(t, seed, "push", "-q", "-u", "origin", "HEAD")

	st := machine(t)
	key := open(t, st, remote)
	put(t, st, key, "12121212-aaaa-bbbb-cccc-000000000001", "x")
	publish(t, st)
	files := testutil.RemoteFiles(t, remote)
	if !strings.Contains(files, "README.md") || !strings.Contains(files, markerFile) {
		t.Fatalf("remote files:\n%s", files)
	}
}

func TestMigrateEmptyFormat1Store(t *testing.T) {
	remote := testutil.Remote(t)
	legacyStore(t, remote, "", "")
	st := machine(t)
	if err := st.Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	if n, err := st.Migrate(pass); err != nil || n != 0 {
		t.Fatalf("migrate: n=%d err=%v", n, err)
	}
	if _, err := st.Unlock(pass); err != nil {
		t.Fatalf("unlock after migrating an empty store: %v", err)
	}
}

// TestLargeBundlesAreSplit: no store file may exceed what git hosts accept.
func TestLargeBundlesAreSplit(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)
	id := "13131313-aaaa-bbbb-cccc-000000000001"
	big := strings.Repeat("x", partSize*2+123)
	put(t, st, key, id, big)
	parts, _ := filepath.Glob(st.path(sessionsDir, id, "data.age*"))
	if len(parts) != 3 {
		t.Fatalf("parts = %v, want 3", parts)
	}
	for _, p := range parts {
		if info, _ := os.Stat(p); info.Size() > partSize {
			t.Fatalf("%s is %d bytes", p, info.Size())
		}
	}
	if data, err := payload(t, st, key, id); err != nil || string(data) != big {
		t.Fatalf("reassembled payload wrong (len %d, %v)", len(data), err)
	}
	// Rewriting with a small bundle removes the stale parts.
	put(t, st, key, id, "small")
	if parts, _ := filepath.Glob(st.path(sessionsDir, id, "data.age*")); len(parts) != 1 {
		t.Fatalf("stale parts left: %v", parts)
	}
	if data, _ := payload(t, st, key, id); string(data) != "small" {
		t.Fatalf("payload after rewrite: %q", data)
	}
}

// TestCompact: the history becomes one commit with the current files,
// other machines carry on with it, and a push made meanwhile is never
// overwritten.
func TestCompact(t *testing.T) {
	remote := testutil.Remote(t)
	commits := func() string { return testutil.Git(t, remote, "rev-list", "--count", "HEAD") }
	a := machine(t)
	keyA := open(t, a, remote)
	b := machine(t)
	keyB := open(t, b, remote)
	kept, gone := "57575757-aaaa-bbbb-cccc-000000000001", "57575757-aaaa-bbbb-cccc-000000000002"
	put(t, a, keyA, kept, "v1")
	put(t, a, keyA, gone, "x")
	publish(t, a)
	put(t, a, keyA, kept, "v2")
	publish(t, a)
	if _, err := a.Remove(gone); err != nil {
		t.Fatal(err)
	}
	publish(t, a)
	before := commits()

	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	r, err := a.Compact()
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(r.Commits) != before || commits() != "1" {
		t.Fatalf("compacted %d commits (remote had %s), remote now has %s", r.Commits, before, commits())
	}
	if data, err := payload(t, a, keyA, kept); err != nil || string(data) != "v2" {
		t.Fatalf("after compact: %q %v", data, err)
	}

	// Machine B, synced before, follows the new history and pushes on it.
	if err := b.Sync(); err != nil {
		t.Fatal(err)
	}
	if data, err := payload(t, b, keyB, kept); err != nil || string(data) != "v2" {
		t.Fatalf("machine B after compact: %q %v", data, err)
	}
	if _, err := payload(t, b, keyB, gone); err == nil {
		t.Fatal("machine B still has the removed session")
	}
	put(t, b, keyB, "57575757-aaaa-bbbb-cccc-000000000003", "from B")
	publish(t, b)

	// B pushes again after A synced: compacting must not overwrite it.
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	put(t, b, keyB, "57575757-aaaa-bbbb-cccc-000000000004", "from B again")
	publish(t, b)
	if _, err := a.Compact(); !errors.Is(err, ErrRemoteMoved) {
		t.Fatalf("compact over an unseen push: %v", err)
	}
	if commits() != "3" {
		t.Fatalf("remote has %s commits, want B's pushes kept", commits())
	}

	// Already compact: nothing to do.
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Compact(); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Compact(); err != nil || r.Commits != 1 {
		t.Fatalf("second compact: %+v %v", r, err)
	}

	folder := Open(Entry{Name: "f", Kind: KindFolder, Dir: t.TempDir()})
	if _, err := folder.Compact(); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("folder store compact: %v", err)
	}
}

// TestPushInParts: unpublished commits go up a few at a time, each part
// reporting its progress, and History counts them.
func TestPushInParts(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)
	for i := 0; i < 12; i++ {
		put(t, st, key, fmt.Sprintf("58585858-aaaa-bbbb-cccc-%012d", i), "x")
		if err := st.Commit("test"); err != nil {
			t.Fatal(err)
		}
	}
	var parts []int
	err := st.PushProgress(func(p UploadProgress) {
		if p.Parts != 3 || p.Done > p.Total {
			t.Errorf("progress %+v", p)
		}
		if len(parts) == 0 || parts[len(parts)-1] != p.Part {
			parts = append(parts, p.Part)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(parts) != "[1 2 3]" {
		t.Fatalf("parts = %v", parts)
	}
	if testutil.Git(t, remote, "rev-parse", "HEAD") != testutil.Git(t, st.Dir, "rev-parse", "HEAD") {
		t.Fatal("remote is not at the clone's HEAD")
	}
	h, err := st.History()
	if err != nil || h.Commits != 13 || h.Bytes == 0 { // -1: a partial clone
		t.Fatalf("history = %+v %v", h, err)
	}
	if _, err := Open(Entry{Name: "f", Kind: KindFolder, Dir: t.TempDir()}).History(); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("folder history: %v", err)
	}
}

// TestBundlesLoadLazily: a clone checks out only metadata; bundles are
// read from git on demand, and rewriting or removing a session leaves no
// stale bundle part in the store.
func TestBundlesLoadLazily(t *testing.T) {
	remote := testutil.Remote(t)
	a := machine(t)
	keyA := open(t, a, remote)
	big, small := "57575757-bbbb-bbbb-cccc-000000000001", "57575757-bbbb-bbbb-cccc-000000000002"
	bigData := strings.Repeat("x", partSize+123) // two parts
	put(t, a, keyA, big, bigData)
	put(t, a, keyA, small, "small")
	publish(t, a)
	if parts, _ := filepath.Glob(a.path(sessionsDir, "*", "data.age*")); len(parts) != 0 {
		t.Fatalf("bundles left in the checkout after a commit: %v", parts)
	}

	b := machine(t)
	keyB := open(t, b, remote)
	if parts, _ := filepath.Glob(b.path(sessionsDir, "*", "data.age*")); len(parts) != 0 {
		t.Fatalf("a new clone checked out bundles: %v", parts)
	}
	if n := testutil.MissingObjects(t, b.Dir); n < 3 { // both bundles' parts
		t.Fatalf("clone has %d missing objects: not partial", n)
	}
	if metas, _, err := b.List(keyB); err != nil || len(metas) != 2 {
		t.Fatalf("list without bundles: %d %v", len(metas), err)
	}
	if data, err := payload(t, b, keyB, big); err != nil || string(data) != bigData {
		t.Fatalf("lazy read: len %d, %v", len(data), err)
	}

	// B shrinks the big session to one part and removes the small one.
	put(t, b, keyB, big, "now small")
	if _, err := b.Remove(small); err != nil {
		t.Fatal(err)
	}
	publish(t, b)
	files := testutil.RemoteFiles(t, remote)
	if strings.Contains(files, big+"/data.age.1") || strings.Contains(files, small) {
		t.Fatalf("stale bundle files in the store:\n%s", files)
	}
	if !strings.Contains(files, big+"/data.age") {
		t.Fatalf("bundle missing from the store:\n%s", files)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if data, err := payload(t, a, keyA, big); err != nil || string(data) != "now small" {
		t.Fatalf("machine A after B's rewrite: %q %v", data, err)
	}
	if out := testutil.Git(t, a.Dir, "status", "--porcelain"); out != "" {
		t.Fatalf("clone dirty: %q", out)
	}
	// A clone made by an older ccsync has every bundle checked out; the
	// next sync drops them from the checkout, losing nothing.
	testutil.Git(t, a.Dir, "sparse-checkout", "disable")
	if _, err := os.Stat(a.path(sessionsDir, big, "data.age")); err != nil {
		t.Fatal("full checkout expected")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.path(sessionsDir, big, "data.age")); !os.IsNotExist(err) {
		t.Fatal("bundle still checked out after sync")
	}
	if data, err := payload(t, a, keyA, big); err != nil || string(data) != "now small" {
		t.Fatalf("after converting the clone: %q %v", data, err)
	}
}

// TestSyncDropsUnpublishedBundles: a bundle written but never committed
// (a push that failed midway) must not survive a sync, or a later commit
// would publish it under metadata it doesn't match.
func TestSyncDropsUnpublishedBundles(t *testing.T) {
	remote := testutil.Remote(t)
	st := machine(t)
	key := open(t, st, remote)
	id := "59595959-aaaa-bbbb-cccc-000000000001"
	put(t, st, key, id, "published")
	publish(t, st)
	put(t, st, key, id, "never published")
	if err := st.Sync(); err != nil {
		t.Fatal(err)
	}
	if parts, _ := filepath.Glob(st.path(sessionsDir, id, "data.age*")); len(parts) != 0 {
		t.Fatalf("unpublished bundle left after sync: %v", parts)
	}
	if data, err := payload(t, st, key, id); err != nil || string(data) != "published" {
		t.Fatalf("payload after sync: %q %v", data, err)
	}
	put(t, st, key, "59595959-aaaa-bbbb-cccc-000000000002", "other")
	publish(t, st)
	if data, err := payload(t, st, key, id); err != nil || string(data) != "published" {
		t.Fatalf("payload after an unrelated push: %q %v", data, err)
	}
}

// TestOlderCloneBecomesPartial: a full clone (made by an older ccsync)
// stops downloading bundles it doesn't read.
func TestOlderCloneBecomesPartial(t *testing.T) {
	remote := testutil.Remote(t)
	a := machine(t)
	keyA := open(t, a, remote)
	put(t, a, keyA, "60606060-aaaa-bbbb-cccc-000000000001", "first")
	publish(t, a)

	old := machine(t)
	testutil.Git(t, "", "clone", "-q", remote, old.Dir) // full, every bundle checked out
	if err := old.Sync(); err != nil {
		t.Fatal(err)
	}
	put(t, a, keyA, "60606060-aaaa-bbbb-cccc-000000000002", "second")
	publish(t, a)
	if err := old.Sync(); err != nil {
		t.Fatal(err)
	}
	if n := testutil.MissingObjects(t, old.Dir); n == 0 {
		t.Fatal("the converted clone downloaded the new bundle")
	}
	key, err := old.Unlock(pass)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := payload(t, old, key, "60606060-aaaa-bbbb-cccc-000000000002"); err != nil || string(data) != "second" {
		t.Fatalf("read after conversion: %q %v", data, err)
	}
}

// TestPrefetch: one fetch brings exactly the asked sessions' current
// bundles — not other sessions', not old versions.
func TestPrefetch(t *testing.T) {
	remote := testutil.Remote(t)
	a := machine(t)
	keyA := open(t, a, remote)
	one, two := "61616161-aaaa-bbbb-cccc-000000000001", "61616161-aaaa-bbbb-cccc-000000000002"
	put(t, a, keyA, one, "old version")
	publish(t, a)
	put(t, a, keyA, one, "current")
	put(t, a, keyA, two, "other")
	publish(t, a)

	b := machine(t)
	keyB := open(t, b, remote)
	before := testutil.MissingObjects(t, b.Dir)
	if err := b.Prefetch([]string{one}); err != nil {
		t.Fatal(err)
	}
	if got := before - testutil.MissingObjects(t, b.Dir); got != 1 {
		t.Fatalf("prefetch brought %d objects, want 1", got)
	}
	if data, err := payload(t, b, keyB, one); err != nil || string(data) != "current" {
		t.Fatalf("payload = %q %v", data, err)
	}
	if err := b.Prefetch([]string{one}); err != nil { // nothing left to fetch
		t.Fatal(err)
	}
}
