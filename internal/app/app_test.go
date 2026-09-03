package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccsync/internal/session"
	"ccsync/internal/store"
)

const pass = "app-test-pass"

// world sets up a claude dir, a bare remote, and an initialized store,
// and returns the claude dir.
func world(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	claudeDir := filepath.Join(base, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CCSYNC_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("CCSYNC_STORE_DIR", filepath.Join(base, "store"))
	remote := filepath.Join(base, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if err := store.Init("file://"+remote, pass); err != nil {
		t.Fatal(err)
	}
	return claudeDir
}

func writeSession(t *testing.T, claudeDir, cwd, id string, age time.Duration) session.Session {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", session.SlugForPath(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, id+".jsonl")
	content := `{"type":"system","cwd":"` + cwd + `"}` + "\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(file, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	sessions, err := session.ListForProject(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("fixture session %s not listed", id)
	return session.Session{}
}

func TestPushRefusesActiveUnlessForced(t *testing.T) {
	claudeDir := world(t)
	s := writeSession(t, claudeDir, "/home/alice/webapp", "77777777-aaaa-bbbb-cccc-000000000001", 0)

	_, _, err := Push(pass, []session.Session{s}, "/home/alice/webapp", false)
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("push of an active session must be refused, got err=%v", err)
	}
	pushed, _, err := Push(pass, []session.Session{s}, "/home/alice/webapp", true)
	if err != nil || len(pushed) != 1 {
		t.Fatalf("forced push failed: pushed=%d err=%v", len(pushed), err)
	}
}

func TestPushSkipsUnchangedContent(t *testing.T) {
	claudeDir := world(t)
	cwd := "/home/alice/webapp"
	s := writeSession(t, claudeDir, cwd, "77777777-aaaa-bbbb-cccc-000000000002", time.Hour)

	pushed, skipped, err := Push(pass, []session.Session{s}, cwd, false)
	if err != nil || len(pushed) != 1 || len(skipped) != 0 {
		t.Fatalf("first push: pushed=%d skipped=%d err=%v", len(pushed), len(skipped), err)
	}

	// Same content again: no new blob, no commit, no error.
	pushed, skipped, err = Push(pass, []session.Session{s}, cwd, false)
	if err != nil || len(pushed) != 0 || len(skipped) != 1 {
		t.Fatalf("re-push: pushed=%d skipped=%d err=%v", len(pushed), len(skipped), err)
	}

	// Changed content is pushed again.
	if err := os.WriteFile(s.File, []byte(`{"type":"system","cwd":"`+cwd+`","more":"data"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(s.File, old, old); err != nil {
		t.Fatal(err)
	}
	pushed, skipped, err = Push(pass, []session.Session{s}, cwd, false)
	if err != nil || len(pushed) != 1 || len(skipped) != 0 {
		t.Fatalf("push after change: pushed=%d skipped=%d err=%v", len(pushed), len(skipped), err)
	}
}
