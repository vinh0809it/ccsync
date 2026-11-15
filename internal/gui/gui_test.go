package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinhvh0809/ccsync/internal/session"
	"github.com/vinhvh0809/ccsync/internal/testutil"
)

const pass = "gui-test-pass"

type env struct {
	srv     *httptest.Server
	token   string
	machine testutil.Machine
	remote  string
}

// setup builds a fresh machine and a bare remote; start runs the server.
func setup(t *testing.T) *env {
	t.Helper()
	m := testutil.NewMachine(t)
	return &env{machine: m, remote: testutil.Remote(t)}
}

// start starts a GUI server (a new run: fresh token, locked).
func (e *env) start(t *testing.T) {
	t.Helper()
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(s.Handler())
	e.token = s.Token()
	t.Cleanup(e.srv.Close)
}

// call hits the API and decodes {ok, data|error}; wantOK asserts the outcome.
func (e *env) call(t *testing.T, method, path string, body any, wantOK bool) json.RawMessage {
	t.Helper()
	reqBody := bytes.NewBuffer(nil)
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
		t.Fatalf("%s %s: bad response (%d): %v", method, path, res.StatusCode, err)
	}
	if out.OK != wantOK {
		t.Fatalf("%s %s: ok=%v (error=%q), want ok=%v", method, path, out.OK, out.Error, wantOK)
	}
	return out.Data
}

func TestTokenAndMethodsEnforced(t *testing.T) {
	e := setup(t)
	e.start(t)
	res, err := http.Get(e.srv.URL + "/api/state") // no token
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("no-token request got %d, want 403", res.StatusCode)
	}
	// State-changing endpoints only accept POST.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/push", nil)
	req.Header.Set("X-CCSync-Token", e.token)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/push got %d, want 405", res.StatusCode)
	}
	// The page itself needs no token (static, holds no data).
	res, err = http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("index got %d, want 200", res.StatusCode)
	}
}

// TestFullFlow drives the sequence the browser UI performs:
// state → setup → projects → local → push → store → pull → remove.
func TestFullFlow(t *testing.T) {
	e := setup(t)
	project := "/home/alice/webapp"
	id := "66666666-aaaa-bbbb-cccc-000000000001"
	testutil.WriteSession(t, e.machine.Claude, session.SlugForPath(project), id,
		testutil.TitleLine("gui-flow"), testutil.CwdLine(project))
	e.start(t)

	var state struct{ Configured, Unlocked bool }
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if state.Configured || state.Unlocked {
		t.Fatalf("fresh state should be locked/unconfigured: %+v", state)
	}
	e.call(t, "POST", "/api/setup", map[string]string{"repoUrl": e.remote, "passphrase": pass}, true)
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if !state.Configured || !state.Unlocked {
		t.Fatalf("after setup: %+v", state)
	}

	var projects []struct{ Path, Slug string }
	json.Unmarshal(e.call(t, "GET", "/api/projects", nil, true), &projects)
	if len(projects) != 1 || projects[0].Path != project {
		t.Fatalf("projects = %+v", projects)
	}
	slug := projects[0].Slug

	var locals []struct {
		ID     string
		Active bool
	}
	json.Unmarshal(e.call(t, "GET", "/api/local?slug="+slug, nil, true), &locals)
	if len(locals) != 1 || locals[0].ID != id || locals[0].Active {
		t.Fatalf("locals = %+v", locals)
	}

	push := map[string]any{"slug": slug, "project": project, "ids": []string{id}}
	e.call(t, "POST", "/api/push", push, true)
	var again struct{ Pushed, Skipped int }
	json.Unmarshal(e.call(t, "POST", "/api/push", push, true), &again)
	if again.Pushed != 0 || again.Skipped != 1 {
		t.Fatalf("re-push should skip unchanged content: %+v", again)
	}

	var metas []struct {
		SessionID string `json:"session_id"`
		Title     string `json:"title"`
	}
	json.Unmarshal(e.call(t, "GET", "/api/store", nil, true), &metas)
	if len(metas) != 1 || metas[0].Title != "gui-flow" {
		t.Fatalf("store = %+v", metas)
	}

	dest := filepath.Join(t.TempDir(), "webapp")
	var pulled struct {
		Installed string
		Unchanged bool
		Backup    string
	}
	json.Unmarshal(e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest}, true), &pulled)
	data, err := os.ReadFile(pulled.Installed)
	if err != nil || !strings.Contains(string(data), `"cwd":"`+testutil.JSONString(dest)+`"`) {
		t.Fatalf("pulled transcript's cwd not rewritten (%v):\n%s", err, data)
	}
	json.Unmarshal(e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest}, true), &pulled)
	if !pulled.Unchanged {
		t.Fatal("identical re-pull should report unchanged")
	}
	// A diverged local copy is refused without force, replaced with a
	// backup with it.
	os.WriteFile(pulled.Installed, []byte("diverged\n"), 0o600)
	e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest}, false)
	json.Unmarshal(e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest, "force": true}, true), &pulled)
	if b, _ := os.ReadFile(pulled.Backup); string(b) != "diverged\n" {
		t.Fatal("forced pull did not back up the local copy")
	}

	// Removal takes exact ids only.
	e.call(t, "POST", "/api/remove", map[string]any{"ids": []string{"66666666"}}, false)
	e.call(t, "POST", "/api/remove", map[string]any{"ids": []string{id}}, true)
	json.Unmarshal(e.call(t, "GET", "/api/store", nil, true), &metas)
	if len(metas) != 0 {
		t.Fatalf("store after remove = %+v", metas)
	}
}

func TestUnlockRejectsWrongPassphrase(t *testing.T) {
	e := setup(t)
	e.start(t)
	e.call(t, "POST", "/api/setup", map[string]string{"repoUrl": e.remote, "passphrase": pass}, true)

	e.start(t) // restart: locked again
	e.call(t, "POST", "/api/unlock", map[string]string{"passphrase": "wrong"}, false)
	e.call(t, "GET", "/api/store", nil, false)
	e.call(t, "POST", "/api/unlock", map[string]string{"passphrase": pass}, true)
	e.call(t, "GET", "/api/store", nil, true)
}
