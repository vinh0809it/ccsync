package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinhvh0809/ccsync/internal/crypto"
	"github.com/vinhvh0809/ccsync/internal/session"
	"github.com/vinhvh0809/ccsync/internal/store"
)

const pass = "app-test-pass"

// world sets up a claude dir, a bare remote, and an initialized store,
// and returns the claude dir and the store key.
func world(t *testing.T) (string, *crypto.Key) {
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
	key, err := Unlock(pass)
	if err != nil {
		t.Fatal(err)
	}
	return claudeDir, key
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
	claudeDir, key := world(t)
	s := writeSession(t, claudeDir, "/home/alice/webapp", "77777777-aaaa-bbbb-cccc-000000000001", 0)

	_, _, err := Push(key, []session.Session{s}, "/home/alice/webapp", false)
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("push of an active session must be refused, got err=%v", err)
	}
	pushed, _, err := Push(key, []session.Session{s}, "/home/alice/webapp", true)
	if err != nil || len(pushed) != 1 {
		t.Fatalf("forced push failed: pushed=%d err=%v", len(pushed), err)
	}
}

func TestPushSkipsUnchangedContent(t *testing.T) {
	claudeDir, key := world(t)
	cwd := "/home/alice/webapp"
	s := writeSession(t, claudeDir, cwd, "77777777-aaaa-bbbb-cccc-000000000002", time.Hour)

	pushed, skipped, err := Push(key, []session.Session{s}, cwd, false)
	if err != nil || len(pushed) != 1 || len(skipped) != 0 {
		t.Fatalf("first push: pushed=%d skipped=%d err=%v", len(pushed), len(skipped), err)
	}

	// Same content again: no new blob, no commit, no error.
	pushed, skipped, err = Push(key, []session.Session{s}, cwd, false)
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
	pushed, skipped, err = Push(key, []session.Session{s}, cwd, false)
	if err != nil || len(pushed) != 1 || len(skipped) != 0 {
		t.Fatalf("push after change: pushed=%d skipped=%d err=%v", len(pushed), len(skipped), err)
	}
}

// remoteFiles lists the files in the remote's HEAD tree.
func remoteFiles(t *testing.T, claudeDir string) string {
	t.Helper()
	remote := filepath.Join(filepath.Dir(claudeDir), "remote.git")
	out, err := exec.Command("git", "-C", remote, "ls-tree", "-r", "--name-only", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

// TestFailedPushIsRetriedNotSkipped: when the remote rejects a push, the
// next push must deliver the session — not report it "already up to date"
// because the local clone kept the unpushed commit (bug in 0.1.0).
func TestFailedPushIsRetriedNotSkipped(t *testing.T) {
	claudeDir, key := world(t)
	remote := filepath.Join(filepath.Dir(claudeDir), "remote.git")
	marker := filepath.Join(remote, "rejected-once")
	hook := "#!/bin/sh\n[ -e '" + marker + "' ] && exit 0\ntouch '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "88888888-aaaa-bbbb-cccc-000000000001"
	s := writeSession(t, claudeDir, "/home/alice/webapp", id, time.Hour)

	if _, _, err := Push(key, []session.Session{s}, "", false); err == nil {
		t.Fatal("first push should fail (remote rejects it)")
	}
	pushed, skipped, err := Push(key, []session.Session{s}, "", false)
	if err != nil || len(pushed) != 1 || len(skipped) != 0 {
		t.Fatalf("second push: pushed=%d skipped=%d err=%v", len(pushed), len(skipped), err)
	}
	if !strings.Contains(remoteFiles(t, claudeDir), id) {
		t.Fatal("session never reached the remote")
	}
}

// TestPushRetriesWhenAnotherMachinePushesFirst: losing the race to another
// machine is retried transparently, and both machines' sessions survive.
func TestPushRetriesWhenAnotherMachinePushesFirst(t *testing.T) {
	claudeDir, key := world(t)
	remote := "file://" + filepath.Join(filepath.Dir(claudeDir), "remote.git")
	cfgA, storeA := os.Getenv("CCSYNC_CONFIG_DIR"), os.Getenv("CCSYNC_STORE_DIR")
	a := writeSession(t, claudeDir, "/home/alice/webapp", "99999999-aaaa-bbbb-cccc-00000000000a", time.Hour)
	b := writeSession(t, claudeDir, "/home/alice/webapp", "99999999-aaaa-bbbb-cccc-00000000000b", time.Hour)

	// Machine B: its own store clone, same remote.
	base := t.TempDir()
	cfgB, storeB := filepath.Join(base, "config"), filepath.Join(base, "store")
	t.Setenv("CCSYNC_CONFIG_DIR", cfgB)
	t.Setenv("CCSYNC_STORE_DIR", storeB)
	if err := store.Init(remote, pass); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCSYNC_CONFIG_DIR", cfgA)
	t.Setenv("CCSYNC_STORE_DIR", storeA)

	raced := false
	beforePush = func() {
		if raced {
			return
		}
		raced = true
		t.Setenv("CCSYNC_CONFIG_DIR", cfgB)
		t.Setenv("CCSYNC_STORE_DIR", storeB)
		defer func() {
			t.Setenv("CCSYNC_CONFIG_DIR", cfgA)
			t.Setenv("CCSYNC_STORE_DIR", storeA)
		}()
		if _, _, err := Push(key, []session.Session{b}, "", false); err != nil {
			t.Errorf("machine B push: %v", err)
		}
	}
	t.Cleanup(func() { beforePush = func() {} })

	pushed, _, err := Push(key, []session.Session{a}, "", false)
	if err != nil || len(pushed) != 1 || !raced {
		t.Fatalf("push A: pushed=%d raced=%v err=%v", len(pushed), raced, err)
	}
	files := remoteFiles(t, claudeDir)
	if !strings.Contains(files, a.ID) || !strings.Contains(files, b.ID) {
		t.Fatalf("remote lost a session:\n%s", files)
	}
}

// TestPullRelocatesWholeSession pushes a session with subagents, a
// persisted tool result and subdirectory cwds, then pulls it on a machine
// where the project lives elsewhere.
func TestPullRelocatesWholeSession(t *testing.T) {
	claudeDir, key := world(t)
	cwd, id := "/home/alice/webapp", "aaaaaaaa-aaaa-bbbb-cccc-000000000001"
	s := writeSession(t, claudeDir, cwd, id, time.Hour)
	sdir := strings.TrimSuffix(s.File, ".jsonl")
	os.MkdirAll(filepath.Join(sdir, "subagents"), 0o700)
	os.MkdirAll(filepath.Join(sdir, "tool-results"), 0o700)
	os.WriteFile(filepath.Join(sdir, "subagents", "agent-x.jsonl"), []byte(`{"cwd":"`+cwd+`"}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(sdir, "tool-results", "r.txt"), []byte("output"), 0o600)
	extra := `{"cwd":"` + cwd + `/api"}` + "\n" + `{"content":"saved to: ` + filepath.ToSlash(filepath.Join(sdir, "tool-results", "r.txt")) + `"}` + "\n"
	f, _ := os.OpenFile(s.File, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(extra)
	f.Close()
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{s.File, filepath.Join(sdir, "subagents", "agent-x.jsonl"), filepath.Join(sdir, "tool-results", "r.txt")} {
		os.Chtimes(p, old, old)
	}
	if _, _, err := Push(key, []session.Session{s}, "", false); err != nil {
		t.Fatal(err)
	}

	// Machine B: a different Claude dir, project at a different path.
	claudeB := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeB)
	dest := filepath.Join(t.TempDir(), "webapp")
	os.MkdirAll(dest, 0o755)
	res, err := Pull(key, "aaaaaaaa", dest, false)
	if err != nil {
		t.Fatal(err)
	}
	main, _ := os.ReadFile(res.Target)
	newDir := strings.TrimSuffix(res.Target, ".jsonl")
	for _, want := range []string{`"cwd":"` + res.Dest + `"`, `"cwd":"` + res.Dest + `/api"`, filepath.ToSlash(filepath.Join(newDir, "tool-results", "r.txt"))} {
		if !strings.Contains(string(main), want) {
			t.Errorf("pulled transcript lacks %s:\n%s", want, main)
		}
	}
	if strings.Contains(string(main), cwd) {
		t.Errorf("old project path left in transcript:\n%s", main)
	}
	if b, err := os.ReadFile(filepath.Join(newDir, "subagents", "agent-x.jsonl")); err != nil || !strings.Contains(string(b), res.Dest) {
		t.Errorf("subagent transcript missing or not relocated: %s %v", b, err)
	}
	// Pulling again is a no-op, not an error.
	if res, err := Pull(key, "aaaaaaaa", dest, false); err != nil || !res.Unchanged {
		t.Fatalf("re-pull: %+v %v", res, err)
	}
}
