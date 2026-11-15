// Package gui serves ccsync's local web interface.
//
// The server binds to 127.0.0.1 only. Every /api/ request must carry a
// random per-run token (handed out in the URL the browser is opened with),
// so other local users on a shared machine cannot drive the API. The
// unlocked store key is held in memory and never written anywhere.
package gui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "embed"

	"github.com/vinhvh0809/ccsync/internal/app"
	"github.com/vinhvh0809/ccsync/internal/crypto"
	"github.com/vinhvh0809/ccsync/internal/session"
	"github.com/vinhvh0809/ccsync/internal/store"
)

//go:embed index.html
var indexHTML []byte

type Server struct {
	mu    sync.Mutex
	key   *crypto.Key // set after a successful unlock
	token string

	// op serializes everything that touches the store clone: two git
	// operations must never run on it at once.
	op sync.Mutex
}

func NewServer() (*Server, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	return &Server{token: hex.EncodeToString(raw)}, nil
}

func (s *Server) Token() string { return s.token }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	api := func(path string, h http.HandlerFunc) {
		mux.HandleFunc(path, s.requireToken(func(w http.ResponseWriter, r *http.Request) {
			s.op.Lock()
			defer s.op.Unlock()
			h(w, r)
		}))
	}
	api("/api/state", s.handleState)
	api("/api/setup", s.handleSetup)
	api("/api/unlock", s.handleUnlock)
	api("/api/projects", s.handleProjects)
	api("/api/local", s.handleLocal)
	api("/api/store", s.handleStore)
	api("/api/push", s.handlePush)
	api("/api/pull", s.handlePull)
	return mux
}

// Listen binds the GUI server to 127.0.0.1:port (0 picks a free port) and
// returns the listener plus the tokenized URL front-ends should open.
func Listen(port int) (net.Listener, *Server, string, error) {
	store.NoPrompt = true
	s, err := NewServer()
	if err != nil {
		return nil, nil, "", err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, nil, "", err
	}
	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr().String(), s.token)
	return ln, s, url, nil
}

// Run serves the GUI in browser mode and opens the browser unless
// openBrowser is false. Blocks forever.
func Run(port int, openBrowser bool) error {
	ln, s, url, err := Listen(port)
	if err != nil {
		return err
	}
	fmt.Printf("ccsync GUI: %s\n(keep this terminal open; Ctrl-C to stop)\n", url)
	if openBrowser {
		browse(url)
	}
	return http.Serve(ln, s.Handler())
}

func browse(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func (s *Server) requireToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-CCSync-Token")
		if got == "" {
			got = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			fail(w, http.StatusForbidden, fmt.Errorf("bad or missing token — reopen the URL ccsync printed"))
			return
		}
		h(w, r)
	}
}

func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) unlocked() (*crypto.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == nil {
		return nil, fmt.Errorf("locked — unlock with the store passphrase first")
	}
	return s.key, nil
}

func (s *Server) setKey(k *crypto.Key) {
	s.mu.Lock()
	s.key = k
	s.mu.Unlock()
}

// --- handlers ---

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	cfg, err := store.LoadConfig()
	s.mu.Lock()
	unlocked := s.key != nil
	s.mu.Unlock()
	state := map[string]any{
		"configured": err == nil,
		"unlocked":   unlocked,
	}
	if cfg != nil {
		state["repoUrl"] = cfg.RepoURL
	}
	ok(w, state)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoURL    string `json:"repoUrl"`
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &req); err != nil || req.RepoURL == "" || req.Passphrase == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("repoUrl and passphrase are required"))
		return
	}
	if err := store.Init(req.RepoURL, req.Passphrase); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.unlock(w, req.Passphrase)
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &req); err != nil || req.Passphrase == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("passphrase is required"))
		return
	}
	s.unlock(w, req.Passphrase)
}

// unlock opens the store key. A format-1 store is not migrated here:
// migration locks out machines still on ccsync 0.1.x, so it is an explicit
// `ccsync migrate`.
func (s *Server) unlock(w http.ResponseWriter, pass string) {
	key, err := app.Unlock(pass)
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	s.setKey(key)
	ok(w, nil)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := session.ListProjects()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, projects)
}

func (s *Server) handleLocal(w http.ResponseWriter, r *http.Request) {
	// Addressed by the on-disk slug (from /api/projects), not by re-deriving
	// it from a path — derivation is lossy for truncated+hashed slugs.
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("slug query param is required"))
		return
	}
	sessions, err := session.ListForSlug(slug)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	type row struct {
		session.Session
		Active bool `json:"Active"`
	}
	rows := make([]row, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, row{s, time.Since(s.Mtime) < app.ActiveGrace})
	}
	ok(w, rows)
}

func (s *Server) handleStore(w http.ResponseWriter, r *http.Request) {
	key, err := s.unlocked()
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	if err := store.Sync(); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	metas, _, err := store.List(key)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if metas == nil {
		metas = []store.Meta{}
	}
	ok(w, metas)
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	key, err := s.unlocked()
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	var req struct {
		Slug    string   `json:"slug"`
		Project string   `json:"project"` // display path; fallback cwd only
		IDs     []string `json:"ids"`
		Force   bool     `json:"force"`
	}
	if err := decode(r, &req); err != nil || req.Slug == "" || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, fmt.Errorf("slug and ids are required"))
		return
	}
	locals, err := session.ListForSlug(req.Slug)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	byID := map[string]session.Session{}
	for _, s := range locals {
		byID[s.ID] = s
	}
	seen := map[string]bool{}
	var selected []session.Session
	for _, id := range req.IDs {
		s, found := byID[id]
		if !found {
			fail(w, http.StatusBadRequest, fmt.Errorf("no local session %s in %s", id, req.Slug))
			return
		}
		if !seen[id] {
			seen[id] = true
			selected = append(selected, s)
		}
	}
	pushed, skipped, err := app.Push(key, selected, req.Project, req.Force)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"pushed": len(pushed), "skipped": len(skipped)})
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	key, err := s.unlocked()
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	var req struct {
		ID    string `json:"id"`
		Dest  string `json:"dest"`
		Force bool   `json:"force"`
	}
	if err := decode(r, &req); err != nil || req.ID == "" || req.Dest == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("id and dest are required"))
		return
	}
	res, err := app.Pull(key, req.ID, req.Dest, req.Force)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{
		"installed": res.Target,
		"backup":    res.Backup,
		"unchanged": res.Unchanged,
		"resume":    fmt.Sprintf("cd %s && claude --resume %s", shellQuote(res.Dest), res.Meta.SessionID),
	})
}

// shellQuote quotes p for the user's shell when it needs it.
func shellQuote(p string) string {
	if runtime.GOOS == "windows" {
		return `"` + p + `"`
	}
	if strings.IndexFunc(p, func(r rune) bool {
		return !(r == '/' || r == '-' || r == '_' || r == '.' || r == '~' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
