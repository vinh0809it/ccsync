package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
	"github.com/vinh0809it/ccsync/internal/testutil"
)

const pass = "app-test-pass"

// project is a stand-in project path; transcripts only record it.
const project = "/home/alice/webapp"

// machine switches to a fresh simulated computer joined to remote.
func machine(t *testing.T, remote string) (testutil.Machine, *Client) {
	t.Helper()
	m := testutil.NewMachine(t)
	c, err := Stores{}.Add(NewStore{Name: "test", Location: remote}, pass)
	if err != nil {
		t.Fatal(err)
	}
	return m, c
}

// localSession writes a transcript for project and returns it as listed.
func localSession(t *testing.T, m testutil.Machine, id string, lines ...string) session.Session {
	t.Helper()
	lines = append([]string{testutil.CwdLine(project)}, lines...)
	file := testutil.WriteSession(t, m.Claude, session.SlugForPath(project), id, lines...)
	s, err := session.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPushRefusesActiveUnlessForced(t *testing.T) {
	m, c := machine(t, testutil.Remote(t))
	s := localSession(t, m, "77777777-aaaa-bbbb-cccc-000000000001")
	now := time.Now()
	os.Chtimes(s.File, now, now)

	if res, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(res.Active) != 1 || len(res.Pushed) != 0 {
		t.Fatalf("an active session must be held back: %+v %v", res, err)
	}
	if res, err := c.Push([]session.Session{s}, "", PushOptions{IncludeActive: true}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("forced push: %+v %v", res, err)
	}
}

func TestPushSkipsUnchangedContent(t *testing.T) {
	m, c := machine(t, testutil.Remote(t))
	s := localSession(t, m, "77777777-aaaa-bbbb-cccc-000000000002", `{"type":"user","uuid":"u1"}`)

	if res, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("first push: %+v %v", res, err)
	}
	if res, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(res.Pushed) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("re-push: %+v %v", res, err)
	}
	appendLine(t, s.File, `{"type":"user","uuid":"u2"}`)
	if res, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("push after change: %+v %v", res, err)
	}
}

// TestFailedPushIsRetriedNotSkipped: when the remote rejects a push, the
// next push must deliver the session — not report it "already up to date"
// because the local clone kept the unpushed commit (bug in 0.1.0).
func TestFailedPushIsRetriedNotSkipped(t *testing.T) {
	remote := testutil.Remote(t)
	m, c := machine(t, remote)
	testutil.RejectFirstPush(t, remote)
	s := localSession(t, m, "88888888-aaaa-bbbb-cccc-000000000001")

	if _, err := c.Push([]session.Session{s}, "", PushOptions{}); err == nil {
		t.Fatal("first push should fail (remote rejects it)")
	}
	if res, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("second push: %+v %v", res, err)
	}
	if !strings.Contains(testutil.RemoteFiles(t, remote), s.ID) {
		t.Fatal("session never reached the remote")
	}
}

// TestPushRetriesWhenAnotherMachinePushesFirst: losing the race to another
// machine is retried transparently, and both machines' sessions survive.
func TestPushRetriesWhenAnotherMachinePushesFirst(t *testing.T) {
	remote := testutil.Remote(t)
	b, clientB := machine(t, remote)
	sb := localSession(t, b, "99999999-aaaa-bbbb-cccc-00000000000b")
	a, clientA := machine(t, remote)
	sa := localSession(t, a, "99999999-aaaa-bbbb-cccc-00000000000a")

	raced := false
	beforePush = func() {
		if raced {
			return
		}
		raced = true
		b.Use(t)
		defer a.Use(t)
		if _, err := clientB.Push([]session.Session{sb}, "", PushOptions{}); err != nil {
			t.Errorf("machine B push: %v", err)
		}
	}
	t.Cleanup(func() { beforePush = func() {} })

	if res, err := clientA.Push([]session.Session{sa}, "", PushOptions{}); err != nil || len(res.Pushed) != 1 || !raced {
		t.Fatalf("push A: %+v raced=%v err=%v", res, raced, err)
	}
	files := testutil.RemoteFiles(t, remote)
	if !strings.Contains(files, sa.ID) || !strings.Contains(files, sb.ID) {
		t.Fatalf("remote lost a session:\n%s", files)
	}
}

// TestPullRelocatesWholeSession pushes a session with a subagent, a
// persisted tool result and subdirectory cwds, then pulls it on a machine
// where the project lives elsewhere.
func TestPullRelocatesWholeSession(t *testing.T) {
	remote := testutil.Remote(t)
	a, clientA := machine(t, remote)
	id := "aaaaaaaa-aaaa-bbbb-cccc-000000000001"
	sdir := filepath.Join(a.Claude, "projects", session.SlugForPath(project), id)
	testutil.WriteFile(t, filepath.Join(sdir, "subagents", "agent-x.jsonl"), testutil.CwdLine(project)+"\n")
	testutil.WriteFile(t, filepath.Join(sdir, "tool-results", "r.txt"), "output")
	s := localSession(t, a, id,
		testutil.CwdLine(project+"/api"),
		`{"content":"saved to: `+testutil.JSONString(filepath.Join(sdir, "tool-results", "r.txt"))+`"}`)
	if _, err := clientA.Push([]session.Session{s}, "", PushOptions{}); err != nil {
		t.Fatal(err)
	}

	_, clientB := machine(t, remote)
	dest := filepath.Join(t.TempDir(), "webapp")
	os.MkdirAll(dest, 0o755)
	res, err := clientB.Pull("aaaaaaaa", dest, false)
	if err != nil {
		t.Fatal(err)
	}
	main, _ := os.ReadFile(res.Target)
	newDir := strings.TrimSuffix(res.Target, ".jsonl")
	for _, want := range []string{
		`"cwd":"` + testutil.JSONString(res.Dest) + `"`,
		`"cwd":"` + testutil.JSONString(res.Dest) + `/api"`,
		testutil.JSONString(filepath.Join(newDir, "tool-results", "r.txt")),
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("pulled transcript lacks %s:\n%s", want, main)
		}
	}
	if strings.Contains(string(main), project) {
		t.Errorf("old project path left in transcript:\n%s", main)
	}
	if b, err := os.ReadFile(filepath.Join(newDir, "subagents", "agent-x.jsonl")); err != nil || !strings.Contains(string(b), testutil.JSONString(res.Dest)) {
		t.Errorf("subagent transcript missing or not relocated: %s %v", b, err)
	}
	if res, err := clientB.Pull("aaaaaaaa", dest, false); err != nil || !res.Unchanged {
		t.Fatalf("re-pull: %+v %v", res, err)
	}
}

func TestRemove(t *testing.T) {
	remote := testutil.Remote(t)
	m, c := machine(t, remote)
	s := localSession(t, m, "bbbbbbbb-aaaa-bbbb-cccc-000000000001")
	if _, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Remove([]string{"ffffffff"}); err == nil {
		t.Fatal("removing an unknown id should fail")
	}
	removed, err := c.Remove([]string{"bbbbbbbb"})
	if err != nil || len(removed) != 1 {
		t.Fatalf("Remove = %+v %v", removed, err)
	}
	if strings.Contains(testutil.RemoteFiles(t, remote), s.ID) {
		t.Fatal("session still in the remote")
	}
	if _, err := os.Stat(s.File); err != nil {
		t.Fatal("Remove touched the local copy")
	}
}

func TestResumeCommandQuotes(t *testing.T) {
	r := PullResult{Dest: "/home/a/my project", Meta: store.Meta{SessionID: "x"}}
	if got := r.ResumeCommand(); got != "cd '/home/a/my project' && claude --resume x" && got != `cd "/home/a/my project" && claude --resume x` {
		t.Fatalf("ResumeCommand = %s", got)
	}
}

// TestPushIsFastForwardOnly plays the git-like scenarios: an outdated copy
// can't overwrite a newer one, and a session continued on two machines is
// rejected until forced.
func TestPushIsFastForwardOnly(t *testing.T) {
	remote := testutil.Remote(t)
	id := "dddddddd-aaaa-bbbb-cccc-000000000001"
	a, clientA := machine(t, remote)
	sa := localSession(t, a, id, `{"type":"user","uuid":"u1"}`)
	if _, err := clientA.Push([]session.Session{sa}, "", PushOptions{}); err != nil {
		t.Fatal(err)
	}

	// B pulls it, continues it, pushes: a fast-forward.
	_, clientB := machine(t, remote)
	dest := filepath.Join(t.TempDir(), "webapp")
	pulled, err := clientB.Pull(id, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	// Pulled to another path but not continued: nothing to push.
	sb, _ := session.Load(pulled.Target)
	if res, err := clientB.Push([]session.Session{sb}, "", PushOptions{}); err != nil || len(res.Skipped) != 1 {
		t.Fatalf("pushing an untouched pulled copy: %+v %v", res, err)
	}
	appendLine(t, pulled.Target, `{"type":"user","uuid":"u2"}`)
	sb, _ = session.Load(pulled.Target)
	if res, err := clientB.Push([]session.Session{sb}, "", PushOptions{}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("B fast-forward push: %+v %v", res, err)
	}

	// A still has the old copy: pushing it would drop u2.
	a.Use(t)
	res, err := clientA.Push([]session.Session{sa}, "", PushOptions{})
	if err != nil || len(res.Rejected) != 1 || res.Rejected[0].Relation != session.Behind {
		t.Fatalf("outdated push: %+v %v", res, err)
	}
	// A continues its old copy too: now the copies have diverged.
	appendLine(t, sa.File, `{"type":"user","uuid":"u3"}`)
	res, err = clientA.Push([]session.Session{sa}, "", PushOptions{})
	if err != nil || len(res.Rejected) != 1 || res.Rejected[0].Relation != session.Diverged {
		t.Fatalf("diverged push: %+v %v", res, err)
	}
	if res, err = clientA.Push([]session.Session{sa}, "", PushOptions{Overwrite: true}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("forced push: %+v %v", res, err)
	}
}

// appendLine appends a transcript line and backdates the file so the
// session doesn't count as active.
func appendLine(t *testing.T, file, line string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, file, string(data)+line+"\n")
}

func TestBatches(t *testing.T) {
	s := func(size int64) session.Session { return session.Session{Size: size} }
	got := batches([]session.Session{s(40), s(30), s(100), s(10), s(10)}, 64)
	var sizes [][]int64
	for _, b := range got {
		var row []int64
		for _, x := range b {
			row = append(row, x.Size)
		}
		sizes = append(sizes, row)
	}
	want := "[[40] [30] [100] [10 10]]"
	if fmt.Sprint(sizes) != want {
		t.Fatalf("batches = %v, want %s", sizes, want)
	}
}

// TestPushOverUnreadableStoreCopy: an unreadable store copy is reported,
// not fatal to the batch, and replaced when overwriting.
func TestPushOverUnreadableStoreCopy(t *testing.T) {
	m, c := machine(t, testutil.Remote(t))
	bad := localSession(t, m, "cdcdcdcd-aaaa-bbbb-cccc-000000000001", `{"type":"user","uuid":"u1"}`)
	good := localSession(t, m, "cdcdcdcd-aaaa-bbbb-cccc-000000000002", `{"type":"user","uuid":"u1"}`)
	if _, err := c.Push([]session.Session{bad}, "", PushOptions{}); err != nil {
		t.Fatal(err)
	}
	// Corrupt the store copy's bundle (and publish the corruption).
	os.WriteFile(filepath.Join(c.Entry.Dir, "sessions", bad.ID, "data.age"), []byte("garbage"), 0o644)
	testutil.Git(t, c.Entry.Dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qam", "corrupt")
	testutil.Git(t, c.Entry.Dir, "push", "-q")
	appendLine(t, bad.File, `{"type":"user","uuid":"u2"}`)

	res, err := c.Push([]session.Session{bad, good}, "", PushOptions{})
	if err != nil || len(res.Rejected) != 1 || res.Rejected[0].Relation != session.Unknown || len(res.Pushed) != 1 {
		t.Fatalf("push with an unreadable store copy: %+v %v", res, err)
	}
	if res, err := c.Push([]session.Session{bad}, "", PushOptions{Overwrite: true}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
}

// TestPullGuards: a session open in Claude Code isn't replaced, and a
// forced pull keeps the local transcript and companion files.
func TestPullGuards(t *testing.T) {
	remote := testutil.Remote(t)
	a, ca := machine(t, remote)
	id := "efefefef-aaaa-bbbb-cccc-000000000001"
	s := localSession(t, a, id, `{"type":"user","uuid":"u1"}`)
	if _, err := ca.Push([]session.Session{s}, "", PushOptions{}); err != nil {
		t.Fatal(err)
	}

	_, cb := machine(t, remote)
	dest := t.TempDir()
	res, err := cb.Pull(id, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	// Continue it here (diverging from the store), with a subagent file,
	// and leave it "open": just modified.
	companion := strings.TrimSuffix(res.Target, ".jsonl")
	testutil.WriteFile(t, filepath.Join(companion, "subagents", "local.jsonl"), "mine\n")
	data, _ := os.ReadFile(res.Target)
	os.WriteFile(res.Target, append(data, []byte(`{"type":"user","uuid":"local"}`+"\n")...), 0o600)

	if _, err := cb.Pull(id, dest, false); err == nil || !strings.Contains(err.Error(), "open in Claude Code") {
		t.Fatalf("pull over an open session: %v", err)
	}
	forced, err := cb.Pull(id, dest, true)
	if err != nil || forced.Backup == "" {
		t.Fatalf("forced pull: %+v %v", forced, err)
	}
	bak := companion + strings.TrimPrefix(forced.Backup, res.Target)
	if b, err := os.ReadFile(filepath.Join(bak, "subagents", "local.jsonl")); err != nil || string(b) != "mine\n" {
		t.Fatalf("companion files not backed up at %s: %v", bak, err)
	}
}
