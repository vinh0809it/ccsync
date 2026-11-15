package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSlugForPath(t *testing.T) {
	cases := map[string]string{
		"/home/vinhvh":               "-home-vinhvh",
		"/home/user/my.app_v2":       "-home-user-my-app-v2",
		`C:\Users\x\dev`:             "C--Users-x-dev",
		"/data/projects/company/gci": "-data-projects-company-gci",
	}
	for in, want := range cases {
		if got := SlugForPath(in); got != want {
			t.Errorf("SlugForPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// fixture writes a minimal transcript for project path cwd and returns the file.
func fixture(t *testing.T, claudeDir, cwd, id, title string) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", SlugForPath(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"custom-title","customTitle":"` + title + `","sessionId":"` + id + `"}
{"type":"system","cwd":"` + cwd + `","version":"2.1.220","sessionId":"` + id + `"}
{"type":"user","message":"the file lives in ` + cwd + `/src/main.go"}
`
	file := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestListForProjectAndInspect(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	project := "/home/alice/webapp"
	fixture(t, claudeDir, project, "11111111-aaaa-bbbb-cccc-000000000001", "fix-login")
	fixture(t, claudeDir, project, "11111111-aaaa-bbbb-cccc-000000000002", "add-tests")

	sessions, err := ListForProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	byID := map[string]Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	s := byID["11111111-aaaa-bbbb-cccc-000000000001"]
	if s.Title != "fix-login" || s.Cwd != project || s.Version != "2.1.220" {
		t.Fatalf("bad metadata: %+v", s)
	}
	if s.Size == 0 || s.Mtime.IsZero() {
		t.Fatalf("missing stat info: %+v", s)
	}
}

func TestListForProjectMissing(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if _, err := ListForProject("/nowhere/nothing"); err == nil {
		t.Fatal("expected an error for a project with no sessions")
	}
}

func TestListProjects(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	fixture(t, claudeDir, "/home/alice/webapp", "11111111-aaaa-bbbb-cccc-000000000001", "a")
	fixture(t, claudeDir, "/home/alice/api", "11111111-aaaa-bbbb-cccc-000000000002", "b")

	projects, err := ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	paths := map[string]int{}
	for _, p := range projects {
		paths[p.Path] = p.Sessions
	}
	if paths["/home/alice/webapp"] != 1 || paths["/home/alice/api"] != 1 {
		t.Fatalf("bad project recovery: %v", paths)
	}
}

func TestTitleLastRenameWins(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	file := fixture(t, claudeDir, "/x", "11111111-aaaa-bbbb-cccc-000000000003", "first")
	f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"custom-title","customTitle":"renamed"}` + "\n")
	f.Close()
	s, err := Load(file)
	if err != nil || s.Title != "renamed" {
		t.Fatalf("title = %q (err %v), want the latest rename", s.Title, err)
	}
}

func TestRewriteCwd(t *testing.T) {
	oldCwd, newCwd := "/home/alice/webapp", "/Users/bob/dev/webapp"
	data := []byte(`{"type":"system","cwd":"/home/alice/webapp"}
{"type":"user","message":"see /home/alice/webapp/src/main.go"}
{"type":"system","cwd":"/home/alice/webapp/api/v2","gitBranch":"main"}
{"type":"system","cwd":"/home/alice/webapp-other"}
`)
	got := RewriteCwd(data, oldCwd, newCwd)

	for _, want := range []string{`"cwd":"/Users/bob/dev/webapp"`, `"cwd":"/Users/bob/dev/webapp/api/v2"`} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("missing %s in:\n%s", want, got)
		}
	}
	// A sibling directory sharing the prefix is a different project.
	if !bytes.Contains(got, []byte(`"cwd":"/home/alice/webapp-other"`)) {
		t.Fatal("sibling directory was rewritten")
	}
	// Paths inside message content must be left alone.
	if !bytes.Contains(got, []byte("see /home/alice/webapp/src/main.go")) {
		t.Fatal("content path was modified — only structural fields may change")
	}
	// Windows separators are JSON-escaped backslashes.
	win := RewriteCwd([]byte(`{"cwd":"C:\\dev\\app\\sub"}`), `C:\dev\app`, "/srv/app")
	if string(win) != `{"cwd":"/srv/app\\sub"}` {
		t.Fatalf("windows subdir: %s", win)
	}
	if !bytes.Equal(RewriteCwd(data, "", newCwd), data) || !bytes.Equal(RewriteCwd(data, oldCwd, oldCwd), data) {
		t.Fatal("no-op cases changed data")
	}
}

// companionFixture adds a subagent transcript and a persisted tool result
// that the main transcript points at by absolute path, as Claude Code does.
func companionFixture(t *testing.T, file, cwd string) {
	t.Helper()
	dir := strings.TrimSuffix(file, ".jsonl")
	for rel, content := range map[string]string{
		"subagents/agent-a1.jsonl":     `{"cwd":"` + cwd + `/sub","msg":"hi"}` + "\n",
		"subagents/agent-a1.meta.json": `{"agentType":"general-purpose"}`,
		"tool-results/b1.txt":          "big output",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	ref, _ := json.Marshal("Full output saved to: " + filepath.Join(dir, "tool-results", "b1.txt"))
	f.WriteString(`{"type":"user","content":` + string(ref) + "}\n")
	f.Close()
}

func TestBundleRoundTripAndInstall(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	cwd, id := "/home/alice/webapp", "11111111-aaaa-bbbb-cccc-000000000004"
	file := fixture(t, claudeDir, cwd, id, "t")
	companionFixture(t, file, cwd)

	s, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	payload, hash, err := Bundle(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, hash2, _ := Bundle(s); hash2 != hash {
		t.Fatal("bundle hash is not deterministic")
	}
	c, err := Unpack(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Companion) != 3 {
		t.Fatalf("companion files = %d, want 3", len(c.Companion))
	}

	// Install on "another machine" at another path.
	other := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", other)
	dest := "/srv/bob/webapp"
	res, err := Install(id, c, Origin{Cwd: cwd, SessionDir: s.Dir}, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	newDir := filepath.Join(other, "projects", SlugForPath(dest), id)
	main, _ := os.ReadFile(res.Target)
	if !bytes.Contains(main, []byte(`"cwd":"`+dest+`"`)) {
		t.Fatal("transcript cwd not relocated")
	}
	ref, _ := json.Marshal(filepath.Join(newDir, "tool-results", "b1.txt"))
	if !bytes.Contains(main, ref[1:len(ref)-1]) {
		t.Fatalf("tool-result reference not relocated:\n%s", main)
	}
	sub, err := os.ReadFile(filepath.Join(newDir, "subagents", "agent-a1.jsonl"))
	if err != nil || !bytes.Contains(sub, []byte(`"cwd":"`+dest+`/sub"`)) {
		t.Fatalf("subagent transcript not installed/relocated: %s (%v)", sub, err)
	}
	if got, _ := os.ReadFile(filepath.Join(newDir, "tool-results", "b1.txt")); string(got) != "big output" {
		t.Fatal("tool result not installed")
	}

	// Same content again: no-op. Different local content: refused, then
	// replaced with a backup under force.
	if res, err := Install(id, c, Origin{Cwd: cwd, SessionDir: s.Dir}, dest, false); err != nil || !res.Unchanged {
		t.Fatalf("identical re-install: %+v %v", res, err)
	}
	os.WriteFile(res.Target, []byte("local edits\n"), 0o600)
	if _, err := Install(id, c, Origin{Cwd: cwd}, dest, false); err != ErrLocalDiffers {
		t.Fatalf("want ErrLocalDiffers, got %v", err)
	}
	res2, err := Install(id, c, Origin{Cwd: cwd, SessionDir: s.Dir}, dest, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(res2.Backup); string(b) != "local edits\n" {
		t.Fatal("forced install did not keep a backup of the local copy")
	}
	info, _ := os.Stat(res2.Target)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("transcript perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestUnpackRejectsUnsafeEntries(t *testing.T) {
	for _, rel := range []string{"../x", "a/../../x", "/etc/passwd", `a\..\x`, "C:/x", "./a"} {
		payload, _, err := Pack(Contents{Transcript: []byte("x"), Companion: map[string][]byte{rel: []byte("evil")}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Unpack(payload); err == nil {
			t.Errorf("Unpack accepted %q", rel)
		}
	}
}

func TestInstallRejectsUnsafeIDs(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	c := &Contents{Transcript: []byte("x")}
	for _, id := range []string{
		"../../../../home/victim/.config/autostart/evil",
		"..", ".", "a/b", `a\b`, "", "-leading-dash",
	} {
		if _, err := Install(id, c, Origin{}, "/home/bob/app", false); err == nil {
			t.Errorf("Install accepted unsafe id %q", id)
		}
	}
	if _, err := Install("22222222-aaaa-bbbb-cccc-000000000009", c, Origin{}, "/home/bob/app", false); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
}

func TestLongProjectPath(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	long := "/home/alice/" + strings.Repeat("deep/", 50) + "app"
	c := &Contents{Transcript: []byte("x")}
	if _, err := Install("33333333-aaaa-bbbb-cccc-000000000001", c, Origin{}, long, false); err == nil {
		t.Fatal("long path without an existing folder must be refused, not guessed")
	}
	// Once Claude Code has created its (hashed) folder, it is reused.
	hashed := SlugForPath(long)[:maxSlug] + "-abc123"
	os.MkdirAll(filepath.Join(claudeDir, "projects", hashed), 0o700)
	res, err := Install("33333333-aaaa-bbbb-cccc-000000000001", c, Origin{}, long, false)
	if err != nil || filepath.Base(filepath.Dir(res.Target)) != hashed {
		t.Fatalf("got %+v, %v; want install into %s", res, err, hashed)
	}
}
