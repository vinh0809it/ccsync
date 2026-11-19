package session

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vinh0809it/ccsync/internal/testutil"
)

func TestSlugForPath(t *testing.T) {
	cases := map[string]string{
		"/home/alice":            "-home-alice",
		"/home/user/my.app_v2":   "-home-user-my-app-v2",
		`C:\Users\x\dev`:         "C--Users-x-dev",
		"/srv/projects/acme/api": "-srv-projects-acme-api",
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
	return testutil.WriteSession(t, claudeDir, SlugForPath(cwd), id,
		testutil.TitleLine(title),
		testutil.CwdLine(cwd),
		`{"type":"user","message":"the file lives in `+testutil.JSONString(cwd)+`/src/main.go"}`)
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
	// Destination inside the original project: rewritten exactly once.
	nested := RewriteCwd([]byte(`{"cwd":"/a/proj"}{"cwd":"/a/proj/sub"}`), "/a/proj", "/a/proj/copy")
	if string(nested) != `{"cwd":"/a/proj/copy"}{"cwd":"/a/proj/copy/sub"}` {
		t.Fatalf("nested destination: %s", nested)
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
	contents, err := ReadContents(s)
	if err != nil {
		t.Fatal(err)
	}
	payload, hash, err := Pack(contents)
	if err != nil {
		t.Fatal(err)
	}
	if _, hash2, _ := Pack(contents); hash2 != hash {
		t.Fatal("bundle hash is not deterministic")
	}
	c, err := Unpack(bytes.NewReader(payload))
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

	// Same content again: no-op.
	res2, err := Install(id, c, Origin{Cwd: cwd, SessionDir: s.Dir}, dest, false)
	if err != nil || !res2.Unchanged {
		t.Fatalf("identical re-install: %+v %v", res2, err)
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
		if _, err := Unpack(bytes.NewReader(payload)); err == nil {
			t.Errorf("Unpack accepted %q", rel)
		}
	}
}

// transcript builds a transcript holding conversation entries with the
// given uuids.
func transcript(uuids ...string) []byte {
	var b strings.Builder
	b.WriteString(testutil.TitleLine("t") + "\n")
	for _, u := range uuids {
		b.WriteString(`{"type":"user","uuid":"` + u + `","cwd":"/p"}` + "\n")
	}
	return []byte(b.String())
}

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b []string
		want Relation
	}{
		{[]string{"1", "2"}, []string{"1", "2"}, Same},
		{[]string{"1", "2", "3"}, []string{"1", "2"}, Ahead},
		{[]string{"1"}, []string{"1", "2"}, Behind},
		{[]string{"1", "x"}, []string{"1", "y"}, Diverged},
	} {
		if got := Compare(transcript(tc.a...), transcript(tc.b...)); got != tc.want {
			t.Errorf("Compare(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	// Relocated paths and bookkeeping lines don't count; renames do.
	moved := bytes.ReplaceAll(transcript("1", "2"), []byte("/p"), []byte("/elsewhere"))
	moved = append(moved, `{"type":"last-prompt","lastPrompt":"x"}`+"\n"...)
	if got := Compare(moved, transcript("1", "2")); got != Same {
		t.Errorf("relocated copy = %v, want same", got)
	}
	renamed := append(transcript("1", "2"), testutil.TitleLine("renamed")+"\n"...)
	if got := Compare(renamed, transcript("1", "2")); got != Ahead {
		t.Errorf("renamed copy = %v, want ahead", got)
	}
	// No recognizable entries: no evidence either way.
	if got := Compare([]byte(`{"x":1}`), transcript("1")); got != Unknown {
		t.Errorf("unrecognized copy = %v, want unknown", got)
	}
}

// TestInstallFastForwardsOnly: a local copy is replaced only when the
// incoming one has all of its entries; otherwise force is needed, and the
// local copy is backed up.
func TestInstallFastForwardsOnly(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	id, dest := "44444444-aaaa-bbbb-cccc-000000000001", "/home/bob/app"
	incoming := &Contents{Transcript: transcript("1", "2")}
	install := func(local []byte, force bool) (InstallResult, error) {
		t.Helper()
		if local != nil {
			res, _ := Install(id, &Contents{Transcript: local}, Origin{}, dest, true)
			os.WriteFile(res.Target, local, 0o600)
		}
		return Install(id, incoming, Origin{}, dest, force)
	}

	if res, err := install(transcript("1"), false); err != nil || !res.Updated || res.Backup != "" {
		t.Fatalf("older local copy should fast-forward: %+v %v", res, err)
	}
	if _, err := install(transcript("1", "2", "3"), false); err != ErrLocalAhead {
		t.Fatalf("newer local copy: want ErrLocalAhead, got %v", err)
	}
	if _, err := install(transcript("1", "x"), false); err != ErrDiverged {
		t.Fatalf("diverged local copy: want ErrDiverged, got %v", err)
	}
	res, err := install(transcript("1", "x"), true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(res.Backup); !bytes.Equal(b, transcript("1", "x")) {
		t.Fatal("forced install did not back up the local copy")
	}
}

// TestUnpackSkipsUnknownEntries: a bundle from a newer ccsync may carry
// entries this version doesn't know; they are ignored, not fatal.
func TestUnpackSkipsUnknownEntries(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for name, data := range map[string]string{transcriptEntry: "t", "future/thing": "x"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write([]byte(data))
	}
	tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(tarBuf.Bytes())
	zw.Close()
	c, err := Unpack(bytes.NewReader(gz.Bytes()))
	if err != nil || string(c.Transcript) != "t" || len(c.Companion) != 0 {
		t.Fatalf("Unpack = %+v, %v", c, err)
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

// TestResolveProjectThroughSymlinks: Claude Code files sessions under the
// real path, also for a destination that doesn't exist yet.
func TestResolveProjectThroughSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	real, _ = filepath.EvalSymlinks(real)
	parent, _ := filepath.EvalSymlinks(filepath.Dir(link))
	for in, want := range map[string]string{
		link:                                 real,
		filepath.Join(link, "new", "proj"):   filepath.Join(real, "new", "proj"),
		filepath.Join(link, "..", "nowhere"): filepath.Join(parent, "nowhere"),
	} {
		if got, err := ResolveProject(in); err != nil || got != want {
			t.Errorf("ResolveProject(%s) = %s, %v; want %s", in, got, err, want)
		}
	}
}

// TestLongProjectPathAmbiguous: two long paths sharing the kept prefix are
// told apart by the cwd their sessions recorded.
func TestLongProjectPathAmbiguous(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	base := "/home/alice/" + strings.Repeat("deep/", 50)
	a, b := base+"app-a", base+"app-b"
	for i, p := range []string{a, b} {
		folder := SlugForPath(p)[:maxSlug] + "-hash" + string(rune('0'+i))
		testutil.WriteSession(t, claudeDir, folder, "55555555-aaaa-bbbb-cccc-00000000000"+string(rune('0'+i)), testutil.CwdLine(p))
	}
	got, err := projectDir(claudeDir, b)
	if err != nil || !strings.HasSuffix(got, "-hash1") {
		t.Fatalf("projectDir(b) = %s, %v; want the folder whose sessions ran in b", got, err)
	}
	if _, err := projectDir(claudeDir, base+"app-c"); err == nil {
		t.Fatal("an unknown long path was guessed")
	}
}
func TestFingerprint(t *testing.T) {
	a := []byte("{\"type\":\"user\",\"uuid\":\"u1\",\"cwd\":\"/a\"}\n{\"type\":\"custom-title\",\"customTitle\":\"x\"}\n")
	moved := []byte("{\"type\":\"user\",\"uuid\":\"u1\",\"cwd\":\"/b\"}\n{\"type\":\"custom-title\",\"customTitle\":\"x\"}\n")
	more := append(append([]byte{}, a...), "{\"type\":\"user\",\"uuid\":\"u2\"}\n"...)
	if Fingerprint(a) == "" || Fingerprint(a) != Fingerprint(moved) {
		t.Error("copies with the same entries must share a fingerprint")
	}
	if Fingerprint(a) == Fingerprint(more) {
		t.Error("a copy with more entries must have another fingerprint")
	}
	if Fingerprint([]byte("{\"type\":\"summary\"}\n")) != "" {
		t.Error("a transcript without entries has no fingerprint")
	}
}

// TestLoadBigTranscript: a big transcript is read at both ends, and still
// yields the first cwd and version and the latest title.
func TestLoadBigTranscript(t *testing.T) {
	filler := `{"type":"assistant","message":"` + strings.Repeat("x", 4000) + `"}` + "\n"
	title := func(s string) string { return `{"type":"custom-title","customTitle":"` + s + `"}` + "\n" }
	head := `{"type":"user","cwd":"/home/alice/webapp","version":"2.1.0"}` + "\n"
	cases := map[string]struct{ body, want string }{
		"renamed later": {head + title("old") + strings.Repeat(filler, 600) + title("new") + filler, "new"},
		// The last line alone outgrows the tail window.
		"giant last line": {head + title("only") + strings.Repeat(filler, 400) +
			`{"type":"user","message":"` + strings.Repeat("y", 2<<20) + `"}` + "\n", "only"},
		// No cwd near the start: found further in.
		"late cwd": {strings.Repeat(filler, 600) + `{"cwd":"/home/alice/webapp","version":"2.1.0"}` + "\n" +
			strings.Repeat(filler, 400) + title("t"), "t"},
	}
	for name, c := range cases {
		file := filepath.Join(t.TempDir(), "11111111-2222-3333-4444-555555555555.jsonl")
		if err := os.WriteFile(file, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(file)
		if err != nil || s.Title != c.want || s.Cwd != "/home/alice/webapp" || s.Version != "2.1.0" {
			t.Errorf("%s: %+v %v", name, s, err)
		}
	}
}
