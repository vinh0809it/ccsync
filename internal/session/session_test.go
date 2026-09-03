package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugForPath(t *testing.T) {
	cases := map[string]string{
		"/home/vinhvh":                 "-home-vinhvh",
		"/home/user/my.app_v2":         "-home-user-my-app-v2",
		`C:\Users\x\dev`:               "C--Users-x-dev",
		"/data/projects/company/gci":   "-data-projects-company-gci",
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

func TestRewriteCwd(t *testing.T) {
	oldCwd, newCwd := "/home/alice/webapp", "/Users/bob/dev/webapp"
	data := []byte(`{"type":"system","cwd":"/home/alice/webapp"}
{"type":"user","message":"see /home/alice/webapp/src/main.go"}
{"type":"system","cwd":"/home/alice/webapp","gitBranch":"main"}
`)
	got := RewriteCwd(data, oldCwd, newCwd)

	if bytes.Contains(got, []byte(`"cwd":"/home/alice/webapp"`)) {
		t.Fatal("structural cwd was not rewritten")
	}
	if n := bytes.Count(got, []byte(`"cwd":"/Users/bob/dev/webapp"`)); n != 2 {
		t.Fatalf("want 2 rewritten cwd fields, got %d", n)
	}
	// Paths inside message content must be left alone.
	if !bytes.Contains(got, []byte("see /home/alice/webapp/src/main.go")) {
		t.Fatal("content path was modified — only structural fields may change")
	}
	// No-ops.
	if !bytes.Equal(RewriteCwd(data, "", newCwd), data) {
		t.Fatal("empty oldCwd must be a no-op")
	}
	if !bytes.Equal(RewriteCwd(data, oldCwd, oldCwd), data) {
		t.Fatal("same cwd must be a no-op")
	}
}

func TestInstallRejectsUnsafeIDs(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, id := range []string{
		"../../../../home/victim/.config/autostart/evil",
		"..", ".", "a/b", `a\b`, "", "-leading-dash-ok?no:startsWithDash",
	} {
		if _, err := Install(id, []byte("x"), "/home/bob/app", false); err == nil {
			t.Errorf("Install accepted unsafe id %q", id)
		}
	}
	// A normal UUID still works.
	if _, err := Install("22222222-aaaa-bbbb-cccc-000000000009", []byte("x"), "/home/bob/app", false); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
}

func TestInstall(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	id := "22222222-aaaa-bbbb-cccc-000000000001"
	dest := "/home/bob/newplace"

	target, err := Install(id, []byte("line1\n"), dest, false)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(claudeDir, "projects", SlugForPath(dest))
	if !strings.HasPrefix(target, wantDir) {
		t.Fatalf("installed to %s, want under %s", target, wantDir)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("transcript perms = %v, want 0600", info.Mode().Perm())
	}

	if _, err := Install(id, []byte("other\n"), dest, false); err == nil {
		t.Fatal("overwrite without force should fail")
	}
	if _, err := Install(id, []byte("other\n"), dest, true); err != nil {
		t.Fatalf("overwrite with force failed: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "other\n" {
		t.Fatal("force overwrite did not replace content")
	}
}
