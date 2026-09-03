package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccsync/internal/crypto"
)

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

	if err := Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassphrase(pass); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassphrase("wrong"); err == nil {
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
	if err := Put(pass, meta, transcript); err != nil {
		t.Fatal(err)
	}
	if err := Commit("test push"); err != nil {
		t.Fatal(err)
	}
	// Nothing staged now: Commit must refuse.
	if err := Commit("empty"); err == nil {
		t.Fatal("commit with nothing staged should fail")
	}

	metas, err := List(pass)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Title != "secret-work" || metas[0].OriginalCwd != "/home/alice/webapp" {
		t.Fatalf("bad listing: %+v", metas)
	}

	got, data, err := Get(pass, "33333333")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != meta.SessionID || string(data) != string(transcript) {
		t.Fatal("Get returned wrong session or corrupted data")
	}
	if _, _, err := Get(pass, "99999999"); err == nil {
		t.Fatal("unknown id should fail")
	}
}

// TestNothingReadableInRemote is the "public repo" guarantee: no plaintext
// from transcripts or metadata may appear anywhere in the pushed objects.
func TestNothingReadableInRemote(t *testing.T) {
	remote := newRemote(t)
	machine(t)
	if err := Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	transcript := []byte(`{"cwd":"/home/alice/topsecret","note":"MARKER_SECRET_CONTENT"}` + "\n")
	meta := Meta{
		SessionID:    "44444444-aaaa-bbbb-cccc-000000000001",
		Title:        "MARKER_SECRET_TITLE",
		ProjectLabel: "topsecret",
		OriginalCwd:  "/home/alice/topsecret",
		Hostname:     "MARKER_HOSTNAME",
		SessionMtime: time.Now(),
	}
	if err := Put(pass, meta, transcript); err != nil {
		t.Fatal(err)
	}
	if err := Commit("push"); err != nil {
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
	if err := Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	dir, _ := Dir()
	evil := filepath.Join(dir, "sessions", "66666666-aaaa-bbbb-cccc-00000000000e")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	metaJSON := `{"session_id":"../../../../home/victim/.bashrc","title":"forged"}`
	blob, err := crypto.Encrypt(pass, []byte(metaJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "meta.age"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := crypto.Encrypt(pass, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "data.age"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	metas, err := List(pass)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if m.Title == "forged" {
			t.Fatal("forged meta surfaced in List")
		}
	}
	if _, _, err := Get(pass, "66666666"); err == nil {
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
	if err := Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	transcript := []byte(`{"cwd":"/home/alice/webapp"}` + "\n")
	meta := Meta{
		SessionID:    "55555555-aaaa-bbbb-cccc-000000000001",
		Title:        "cross-machine",
		OriginalCwd:  "/home/alice/webapp",
		ProjectLabel: "webapp",
		Hostname:     "machine-a",
		SessionMtime: time.Now(),
	}
	if err := Put(pass, meta, transcript); err != nil {
		t.Fatal(err)
	}
	if err := Commit("from A"); err != nil {
		t.Fatal(err)
	}

	// Machine B: fresh config/store dirs, same remote + passphrase.
	machine(t)
	if err := Init(remote, "wrong-pass"); err == nil {
		t.Fatal("machine B init with wrong passphrase should fail")
	}
	if err := Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	if err := Sync(); err != nil {
		t.Fatal(err)
	}
	got, data, err := Get(pass, "55555555")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "machine-a" || string(data) != string(transcript) {
		t.Fatal("machine B did not receive machine A's session intact")
	}
}
