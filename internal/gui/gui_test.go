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

	"github.com/vinh0809it/ccsync/internal/app"
	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/testutil"
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
		testutil.TitleLine("gui-flow"), testutil.CwdLine(project), `{"type":"user","uuid":"u1"}`)
	e.start(t)

	var state struct{ Configured, Unlocked bool }
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if state.Configured || state.Unlocked {
		t.Fatalf("fresh state should be locked/unconfigured: %+v", state)
	}
	e.call(t, "POST", "/api/setup", map[string]string{"name": "test", "location": e.remote, "passphrase": pass}, true)
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

	var status map[string]string
	json.Unmarshal(e.call(t, "GET", "/api/status", nil, true), &status)
	if len(status) != 1 || status[id] != "same" {
		t.Fatalf("status = %v, want %s: same", status, id)
	}

	// Pull resolves symlinks and short names (macOS /var, Windows 8.3 temp
	// paths); compare against the resolved form.
	dest, err := session.ResolveProject(filepath.Join(t.TempDir(), "webapp"))
	if err != nil {
		t.Fatal(err)
	}
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
	// A local copy continued here (an entry the store lacks) is refused
	// without force, and replaced with a backup with it.
	diverged := string(data) + `{"type":"user","uuid":"local-only"}` + "\n"
	os.WriteFile(pulled.Installed, []byte(diverged), 0o600)
	e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest}, false)
	json.Unmarshal(e.call(t, "POST", "/api/pull", map[string]any{"id": id, "dest": dest, "force": true}, true), &pulled)
	if b, _ := os.ReadFile(pulled.Backup); string(b) != diverged {
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
	e.call(t, "POST", "/api/setup", map[string]string{"name": "test", "location": e.remote, "passphrase": pass}, true)

	e.start(t) // restart: locked again
	e.call(t, "POST", "/api/unlock", map[string]string{"passphrase": "wrong"}, false)
	e.call(t, "GET", "/api/store", nil, false)
	e.call(t, "POST", "/api/unlock", map[string]string{"passphrase": pass}, true)
	e.call(t, "GET", "/api/store", nil, true)
}

// TestStoresFlow: add two stores, switch between them, forget one.
func TestStoresFlow(t *testing.T) {
	e := setup(t)
	e.start(t)
	second := testutil.Remote(t)
	e.call(t, "POST", "/api/setup", map[string]string{"name": "work", "location": e.remote, "passphrase": "w"}, true)
	e.call(t, "POST", "/api/lock", map[string]string{}, true)
	e.call(t, "POST", "/api/setup", map[string]string{"name": "home", "location": second, "passphrase": "h"}, true)

	var state struct {
		Stores   []struct{ Name string }
		Current  string
		Unlocked bool
		Store    struct{ Name string }
	}
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if len(state.Stores) != 2 || state.Current != "home" || state.Store.Name != "home" {
		t.Fatalf("state after two setups: %+v", state)
	}

	// Switch: lock, then unlock the other store with its own passphrase.
	e.call(t, "POST", "/api/lock", map[string]string{}, true)
	e.call(t, "GET", "/api/store", nil, false)
	e.call(t, "POST", "/api/unlock", map[string]string{"store": "work", "passphrase": "h"}, false)
	e.call(t, "POST", "/api/unlock", map[string]string{"store": "work", "passphrase": "w"}, true)
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if state.Store.Name != "work" || state.Current != "work" {
		t.Fatalf("after switching: %+v", state)
	}

	// Forgetting the open store locks the UI and removes it from the list.
	// A machine without any Claude Code project yet lists an empty array.
	if got := string(e.call(t, "GET", "/api/projects", nil, true)); got != "[]" {
		t.Fatalf("projects on an empty machine = %s, want []", got)
	}

	e.call(t, "POST", "/api/forget", map[string]string{"store": "work"}, true)
	json.Unmarshal(e.call(t, "GET", "/api/state", nil, true), &state)
	if state.Unlocked || len(state.Stores) != 1 || state.Stores[0].Name != "home" {
		t.Fatalf("after forget: %+v", state)
	}
}

// TestTokenNeverInURL: the page gets the token by trading a one-time code;
// a token in the query string is not accepted; foreign Host names are
// refused (DNS rebinding).
func TestTokenNeverInURL(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	exchange := func(code string) (int, string) {
		res, err := http.Post(srv.URL+"/api/session", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Data struct{ Token string } `json:"data"`
		}
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out.Data.Token
	}
	if code, _ := exchange("wrong"); code != http.StatusForbidden {
		t.Fatalf("wrong code: %d", code)
	}
	code := s.Code()
	if status, tok := exchange(code); status != http.StatusOK || tok != s.Token() {
		t.Fatalf("exchange: %d %q", status, tok)
	}
	if status, _ := exchange(code); status != http.StatusForbidden {
		t.Fatal("the one-time code worked twice")
	}

	res, err := http.Get(srv.URL + "/api/state?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("token in the query string accepted: %d", res.StatusCode)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Host = "evil.example:80"
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host accepted: %d", res.StatusCode)
	}
	res, _ = http.Get(srv.URL + "/")
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'none'") ||
		res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers missing: %v", res.Header)
	}
}

// TestPullManyAPI: plan and pull every store session through the API,
// with a folder mapping.
func TestPullManyAPI(t *testing.T) {
	e := setup(t)
	project := "/home/zz-ghost/dev/web"
	testutil.WriteSession(t, e.machine.Claude, session.SlugForPath(project), "77770000-aaaa-bbbb-cccc-000000000001",
		testutil.TitleLine("many"), testutil.CwdLine(project), `{"type":"user","uuid":"u1"}`)
	e.start(t)
	e.call(t, "POST", "/api/setup", map[string]string{"name": "s", "location": e.remote, "passphrase": pass}, true)
	var projects []struct{ Slug string }
	json.Unmarshal(e.call(t, "GET", "/api/projects", nil, true), &projects)
	e.call(t, "POST", "/api/push", map[string]any{"slug": projects[0].Slug, "ids": []string{"77770000-aaaa-bbbb-cccc-000000000001"}}, true)

	var plan struct {
		Plan    []struct{ Dest string }
		Suggest []struct{ From, To string }
	}
	json.Unmarshal(e.call(t, "POST", "/api/pull-plan", map[string]any{}, true), &plan)
	resolved, _ := session.ResolveProject(project) // D:\home\... on Windows
	if len(plan.Plan) != 1 || plan.Plan[0].Dest != resolved || len(plan.Suggest) != 1 || plan.Suggest[0].From != "/home/zz-ghost" {
		t.Fatalf("plan without a mapping: %+v", plan)
	}
	dest, _ := session.ResolveProject(filepath.Join(t.TempDir(), "code"))
	mapping := []map[string]string{{"from": "/home/zz-ghost/dev", "to": dest}}
	json.Unmarshal(e.call(t, "POST", "/api/pull-plan", map[string]any{"map": mapping}, true), &plan)
	if plan.Plan[0].Dest != filepath.Join(dest, "web") {
		t.Fatalf("plan with a mapping: %+v", plan)
	}
	e.call(t, "POST", "/api/pull-plan", map[string]any{"map": []map[string]string{{"from": "/x"}}}, false)

	var res struct {
		Pulled, Unchanged int
		Failed            []struct{ Error string }
	}
	// On this machine the session is still at its own path, so pulling it
	// elsewhere is a fresh install.
	json.Unmarshal(e.call(t, "POST", "/api/pull-many", map[string]any{"map": mapping}, true), &res)
	if res.Pulled != 1 || len(res.Failed) != 0 {
		t.Fatalf("pull-many: %+v", res)
	}
	json.Unmarshal(e.call(t, "POST", "/api/pull-many", map[string]any{"map": mapping}, true), &res)
	if res.Pulled != 0 || res.Unchanged != 1 {
		t.Fatalf("second pull-many: %+v", res)
	}
}

// TestProgressAnswersDuringAnOperation: the page polls progress while a
// push or pull holds the request lock, so progress must not wait for it.
func TestProgressAnswersDuringAnOperation(t *testing.T) {
	e := setup(t)
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(s.Handler())
	e.token = s.Token()
	t.Cleanup(e.srv.Close)

	s.op.Lock() // an operation is running
	defer s.op.Unlock()
	s.progress = app.Progress{Step: "Encrypting", Done: 3, Total: 9}
	var p app.Progress
	if err := json.Unmarshal(e.call(t, "GET", "/api/progress", nil, true), &p); err != nil {
		t.Fatal(err)
	}
	if p != s.progress {
		t.Fatalf("progress = %+v", p)
	}
	e.token = "wrong"
	e.call(t, "GET", "/api/progress", nil, false)
}
