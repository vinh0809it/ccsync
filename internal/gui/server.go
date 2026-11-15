// Package gui serves ccsync's local web interface.
//
// The server binds to 127.0.0.1 only. Every /api/ request must carry a
// random per-run token (handed out in the URL the browser is opened with),
// so other local users on a shared machine cannot drive the API. The
// unlocked store is held in memory and its key is never written anywhere.
package gui

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"

	"github.com/vinh-vh/ccsync/internal/app"
	"github.com/vinh-vh/ccsync/internal/store"
)

//go:embed index.html
var indexHTML []byte

var errLocked = errors.New("locked — unlock with the store passphrase first")

// Server is the GUI's HTTP API.
type Server struct {
	token string
	store *store.Store

	// op serializes every request: they all touch the store clone or the
	// unlocked client, and two git operations must never run at once.
	op     sync.Mutex
	client *app.Client // set after a successful unlock
}

// NewServer returns a server for the default store with a fresh token.
func NewServer() (*Server, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	st, err := store.Open()
	if err != nil {
		return nil, err
	}
	st.Interactive = false
	return &Server{token: hex.EncodeToString(raw), store: st}, nil
}

// Token is the secret every API request must carry.
func (s *Server) Token() string { return s.token }

// Handler routes the page and the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	api := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, s.guard(h))
	}
	api("GET /api/state", s.handleState)
	api("POST /api/setup", s.handleSetup)
	api("POST /api/unlock", s.handleUnlock)
	api("GET /api/projects", s.handleProjects)
	api("GET /api/local", s.handleLocal)
	api("GET /api/store", s.handleStore)
	api("POST /api/push", s.handlePush)
	api("POST /api/pull", s.handlePull)
	api("POST /api/remove", s.handleRemove)
	return mux
}

// guard checks the token and serializes the request.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-CCSync-Token")
		if got == "" {
			got = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			fail(w, http.StatusForbidden, fmt.Errorf("bad or missing token — reopen the URL ccsync printed"))
			return
		}
		s.op.Lock()
		defer s.op.Unlock()
		h(w, r)
	}
}

// Listen binds the GUI server to 127.0.0.1:port (0 picks a free port) and
// returns the listener plus the tokenized URL front-ends should open.
func Listen(port int) (net.Listener, *Server, string, error) {
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
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}
