package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pass = "gui-test-pass"

type env struct {
	srv       *httptest.Server
	token     string
	claudeDir string
	remote    string
}

// setup builds a full fake world: a Claude config dir with one session,
// a bare git remote, and the GUI server on top.
func setup(t *testing.T) *env {
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

	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{srv: srv, token: s.Token(), claudeDir: claudeDir, remote: "file://" + remote}
}

// addSession writes a local transcript for project cwd. Its mtime is aged
// so the session doesn't count as active.
func (e *env) addSession(t *testing.T, cwd, id, title string) {
	t.Helper()
	slug := slugFor(cwd)
	dir := filepath.Join(e.claudeDir, "projects", slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"custom-title","customTitle":"` + title + `","sessionId":"` + id + `"}
{"type":"system","cwd":"` + cwd + `","version":"2.1.220"}
`
	file := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := timeMinus(t, 300)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
}

func timeMinus(t *testing.T, seconds int) time.Time {
	t.Helper()
	return time.Now().Add(-time.Duration(seconds) * time.Second)
}

func slugFor(p string) string {
	var b strings.Builder
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// call hits the API and decodes {ok, data|error}. wantOK asserts the outcome.
func (e *env) call(t *testing.T, method, path string, body any, wantOK bool) json.RawMessage {
	t.Helper()
	var reqBody *bytes.Buffer = bytes.NewBuffer(nil)
	if body != nil {
		json.NewEncoder(reqBody).Encode(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CCSync-Token", e.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: bad response: %v", method, path, err)
	}
	if out.OK != wantOK {
		t.Fatalf("%s %s: ok=%v (error=%q), want ok=%v", method, path, out.OK, out.Error, wantOK)
	}
	return out.Data
}

func TestTokenRequired(t *testing.T) {
	e := setup(t)
	res, err := http.Get(e.srv.URL + "/api/state") // no token header
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("no-token request got %d, want 403", res.StatusCode)
	}
	// The index page itself needs no token (static, holds no data).
	res2, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("index got %d, want 200", res2.StatusCode)
	}
}

// TestFullFlow drives the exact sequence the browser UI performs:
// state → setup → projects → local → push → store → pull → verify on disk.
func TestFullFlow(t *testing.T) {
	e := setup(t)
	project := "/home/alice/webapp"
	id := "66666666-aaaa-bbbb-cccc-000000000001"
	e.addSession(t, project, id, "gui-flow")

	// Fresh machine: not configured yet.
	var state struct {
		Configured bool `json:"configured"`
		Unlocked   bool `json:"unlocked"`
	}
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if state.Configured || state.Unlocked {
		t.Fatalf("fresh state should be locked/unconfigured: %+v", state)
	}

	// Setup configures AND unlocks.
	e.call(t, "POST", "/api/setup", map[string]string{"repoUrl": e.remote, "passphrase": pass}, true)
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if !state.Configured || !state.Unlocked {
		t.Fatalf("after setup: %+v", state)
	}

	// Project discovery finds the real path and the on-disk slug.
	var projects []struct{ Path, Slug string }
	json.Unmarshal(e.call(t, "GET", "/api/projects", nil, true), &projects)
	if len(projects) != 1 || projects[0].Path != project || projects[0].Slug != slugFor(project) {
		t.Fatalf("projects = %+v", projects)
	}
	slug := projects[0].Slug

	// Local sessions list, not active (mtime aged in fixture).
	var locals []struct {
		ID     string
		Title  string
		Active bool
	}
	json.Unmarshal(e.call(t, "GET", "/api/local?slug="+slug, nil, true), &locals)
	if len(locals) != 1 || locals[0].ID != id || locals[0].Active {
		t.Fatalf("locals = %+v", locals)
	}

	// Push, then the store lists it.
	e.call(t, "POST", "/api/push", map[string]any{"slug": slug, "project": project, "ids": []string{id}}, true)

	// An identical second push must be a no-op (content-hash dedup).
	var again struct{ Pushed, Skipped int }
	json.Unmarshal(e.call(t, "POST", "/api/push",
		map[string]any{"slug": slug, "project": project, "ids": []string{id}}, true), &again)
	if again.Pushed != 0 || again.Skipped != 1 {
		t.Fatalf("re-push should skip unchanged content: %+v", again)
	}
	var metas []struct {
		SessionID   string `json:"session_id"`
		Title       string `json:"title"`
		OriginalCwd string `json:"original_cwd"`
	}
	json.Unmarshal(e.call(t, "GET", "/api/store", nil, true), &metas)
	if len(metas) != 1 || metas[0].Title != "gui-flow" || metas[0].OriginalCwd != project {
		t.Fatalf("store = %+v", metas)
	}

	// Pull to a different path; refuse only if the file already exists.
	dest := "/home/alice/elsewhere/webapp"
	var pulled struct{ Installed string }
	json.Unmarshal(e.call(t, "POST", "/api/pull",
		map[string]any{"id": id, "dest": dest}, true), &pulled)
	data, err := os.ReadFile(pulled.Installed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"cwd":"`+dest+`"`)) ||
		bytes.Contains(data, []byte(`"cwd":"`+project+`"`)) {
		t.Fatal("pulled transcript's cwd was not rewritten to the destination")
	}
	// Same pull again without force must fail; with force must succeed.
	e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest}, false)
	e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest, "force": true}, true)
}

func TestUnlockRejectsWrongPassphrase(t *testing.T) {
	e := setup(t)
	e.call(t, "POST", "/api/setup", map[string]string{"repoUrl": e.remote, "passphrase": pass}, true)

	// New server against the same machine state = locked UI after restart.
	s2, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(s2.Handler())
	defer srv2.Close()
	e2 := &env{srv: srv2, token: s2.Token()}

	e2.call(t, "POST", "/api/unlock", map[string]string{"passphrase": "wrong"}, false)
	e2.call(t, "GET", "/api/store", nil, false) // still locked
	e2.call(t, "POST", "/api/unlock", map[string]string{"passphrase": pass}, true)
	e2.call(t, "GET", "/api/store", nil, true)
}
