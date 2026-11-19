// Package gui serves ccsync's local web interface.
//
// The server binds to 127.0.0.1 only and answers only to localhost Host
// names (no DNS rebinding). Every /api/ request must carry a random per-run
// token in a header. The token never appears in a URL or a command line,
// where other local users could read it: the URL the browser is opened with
// carries a one-time code, which the page exchanges for the token. The
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

	"github.com/vinh0809it/ccsync/internal/app"
	"github.com/vinh0809it/ccsync/internal/github"
)

//go:embed index.html
var indexHTML []byte

var errLocked = errors.New("locked — unlock with the store passphrase first")

// Server is the GUI's HTTP API.
type Server struct {
	token  string
	stores app.Stores

	codeMu sync.Mutex
	code   string // one-time code for the first page load; "" once used

	github bool // gh is installed and logged in

	// op serializes every request: they all touch the store clone or the
	// unlocked client, and two git operations must never run at once.
	op     sync.Mutex
	client *app.Client // set after a successful unlock

	// progress is the running operation's, readable while op is held.
	progMu   sync.Mutex
	progress app.Progress
}

// NewServer returns a server with a fresh token. git never prompts for
// credentials from it: nobody could answer.
func NewServer() (*Server, error) {
	token, err := randomHex()
	if err != nil {
		return nil, err
	}
	code, err := randomHex()
	if err != nil {
		return nil, err
	}
	return &Server{token: token, code: code, stores: app.Stores{Interactive: false}, github: github.Available()}, nil
}

func randomHex() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// Token is the secret every API request must carry.
func (s *Server) Token() string { return s.token }

// Code is the one-time code that buys the token (see handleSession).
func (s *Server) Code() string { return s.code }

// Handler routes the page and the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("POST /api/session", s.handleSession)
	// Progress is polled while another request runs, so it isn't serialized.
	mux.HandleFunc("GET /api/progress", s.checkToken(s.handleProgress))
	api := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, s.checkToken(s.serialize(h)))
	}
	api("GET /api/state", s.handleState)
	api("POST /api/setup", s.handleSetup)
	api("POST /api/unlock", s.handleUnlock)
	api("POST /api/lock", s.handleLock)
	api("POST /api/forget", s.handleForget)
	api("GET /api/projects", s.handleProjects)
	api("GET /api/local", s.handleLocal)
	api("GET /api/store", s.handleStore)
	api("GET /api/status", s.handleStatus)
	api("POST /api/push", s.handlePush)
	api("POST /api/pull", s.handlePull)
	api("POST /api/pull-plan", s.handlePullPlan)
	api("POST /api/pull-many", s.handlePullMany)
	api("POST /api/remove", s.handleRemove)
	return localOnly(mux)
}

// localOnly rejects requests whose Host isn't this machine (DNS rebinding)
// and sets security headers on every response.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// handleSession exchanges the one-time code for the token. It works once:
// a code someone else read from a process list is useless after the
// browser (or desktop window) has used it.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	_ = decode(r, &req)
	s.codeMu.Lock()
	valid := s.code != "" && subtle.ConstantTimeCompare([]byte(req.Code), []byte(s.code)) == 1
	if valid {
		s.code = ""
	}
	s.codeMu.Unlock()
	if !valid {
		fail(w, http.StatusForbidden, fmt.Errorf("this link was already used — restart ccsync gui for a new one"))
		return
	}
	ok(w, map[string]string{"token": s.token})
}

// checkToken rejects requests without the session token.
func (s *Server) checkToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-CCSync-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			fail(w, http.StatusForbidden, fmt.Errorf("bad or missing token — reopen the URL ccsync printed"))
			return
		}
		h(w, r)
	}
}

// serialize runs one request at a time.
func (s *Server) serialize(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.op.Lock()
		defer s.op.Unlock()
		h(w, r)
	}
}

// setClient makes c the unlocked store, reporting its progress here.
func (s *Server) setClient(c *app.Client) {
	if c != nil {
		c.OnProgress = func(p app.Progress) {
			s.progMu.Lock()
			s.progress = p
			s.progMu.Unlock()
		}
	}
	s.client = c
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
	url := fmt.Sprintf("http://%s/?c=%s", ln.Addr().String(), s.code)
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
