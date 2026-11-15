package gui

import (
	"fmt"
	"net/http"
	"time"

	"github.com/vinh-vh/ccsync/internal/app"
	"github.com/vinh-vh/ccsync/internal/session"
	"github.com/vinh-vh/ccsync/internal/store"
)

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	cfg, err := store.LoadConfig()
	state := map[string]any{
		"configured": err == nil,
		"unlocked":   s.client != nil,
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
	client, err := app.Setup(s.store, req.RepoURL, req.Passphrase)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.client = client
	ok(w, nil)
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

// unlock opens the store. A format-1 store is not migrated here:
// migration locks out machines still on ccsync 0.1.x, so it is an explicit
// `ccsync migrate`.
func (s *Server) unlock(w http.ResponseWriter, passphrase string) {
	client, err := app.Unlock(s.store, passphrase)
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return
	}
	s.client = client
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

// allProjects is the slug that stands for every project.
const allProjects = "*"

// localSessions lists one project's sessions, addressed by its on-disk
// slug (from /api/projects) rather than a path — deriving the slug is lossy
// for truncated+hashed names — or every project's for allProjects.
func localSessions(slug string) ([]session.Session, error) {
	if slug == allProjects {
		return session.ListAll()
	}
	return session.ListForSlug(slug)
}

func (s *Server) handleLocal(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("slug query param is required"))
		return
	}
	sessions, err := localSessions(slug)
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
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	metas, _, err := s.client.List()
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
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	var req struct {
		Slug      string   `json:"slug"`
		Project   string   `json:"project"` // display path; fallback cwd only
		IDs       []string `json:"ids"`
		Force     bool     `json:"force"`     // include active sessions
		Overwrite bool     `json:"overwrite"` // replace newer or diverged store copies
	}
	if err := decode(r, &req); err != nil || req.Slug == "" || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, fmt.Errorf("slug and ids are required"))
		return
	}
	locals, err := localSessions(req.Slug)
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
	res, err := s.client.Push(selected, req.Project, app.PushOptions{IncludeActive: req.Force, Overwrite: req.Overwrite})
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	type rejection struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Relation string `json:"relation"` // "behind" or "diverged"
		Host     string `json:"host"`
	}
	rejected := make([]rejection, 0, len(res.Rejected))
	for _, r := range res.Rejected {
		rejected = append(rejected, rejection{r.Local.ID, r.Local.Title, r.Relation.String(), r.Store.Hostname})
	}
	ok(w, map[string]any{"pushed": len(res.Pushed), "skipped": len(res.Skipped), "rejected": rejected})
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
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
	res, err := s.client.Pull(req.ID, req.Dest, req.Force)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{
		"installed": res.Target,
		"backup":    res.Backup,
		"unchanged": res.Unchanged,
		"updated":   res.Updated,
		"resume":    res.ResumeCommand(),
	})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decode(r, &req); err != nil || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, fmt.Errorf("ids are required"))
		return
	}
	// Exact ids only: the UI never sends prefixes, and a prefix typo must
	// not delete a different session.
	for _, id := range req.IDs {
		if !session.ValidID(id) || len(id) < 32 {
			fail(w, http.StatusBadRequest, fmt.Errorf("invalid session id %q", id))
			return
		}
	}
	removed, err := s.client.Remove(req.IDs)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, map[string]any{"removed": len(removed)})
}
