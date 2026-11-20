package gui

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/vinh0809it/ccsync/internal/app"
	"github.com/vinh0809it/ccsync/internal/crypto"
	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
)

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	reg, err := s.stores.List()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	type storeInfo struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Location string `json:"location"` // repo URL or folder
		Link     string `json:"link"`     // web page or folder to open, if any
	}
	info := func(e store.Entry) storeInfo {
		kind := string(e.Kind)
		if kind == "" {
			kind = string(store.KindGit)
		}
		return storeInfo{e.Name, kind, e.Location(), storeLink(e)}
	}
	stores := make([]storeInfo, 0, len(reg.Stores))
	for _, e := range reg.Stores {
		stores = append(stores, info(e))
	}
	state := map[string]any{
		"configured": len(stores) > 0,
		"stores":     stores,
		"current":    reg.Current,
		"unlocked":   s.client != nil,
		"github":     s.github, // gh is logged in: offer to create a private repo
	}
	if s.client != nil {
		state["store"] = info(s.client.Entry)
	}
	ok(w, state)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Kind         string `json:"kind"`         // "git" (default) or "folder"
		Location     string `json:"location"`     // repo URL or folder path
		CreateGitHub string `json:"createGitHub"` // create this private GitHub repo instead
		AllowPublic  bool   `json:"allowPublic"`
		Passphrase   string `json:"passphrase"`
	}
	if err := decode(r, &req); err != nil || req.Name == "" || req.Passphrase == "" ||
		(req.Location == "" && req.CreateGitHub == "") {
		fail(w, http.StatusBadRequest, fmt.Errorf("name, a repo or folder, and a passphrase are required"))
		return
	}
	client, err := s.stores.Add(app.NewStore{
		Name: req.Name, Kind: store.Kind(req.Kind), Location: req.Location,
		CreateGitHubRepo: req.CreateGitHub, AllowPublic: req.AllowPublic,
	}, req.Passphrase)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.setClient(client)
	ok(w, nil)
}

func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Store      string `json:"store"` // empty: the current one
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &req); err != nil || req.Passphrase == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("passphrase is required"))
		return
	}
	// A format-1 store is not migrated here: migration locks out machines
	// still on ccsync 0.1.x, so it is an explicit `ccsync migrate`.
	client, err := s.stores.Unlock(req.Store, req.Passphrase)
	if errors.Is(err, crypto.ErrWrongPassphrase) {
		fail(w, http.StatusForbidden, err)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.setClient(client)
	ok(w, nil)
}

// handleLock forgets the unlocked key, e.g. to switch stores.
func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	s.setClient(nil)
	ok(w, nil)
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	res, err := s.client.Compact()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, res)
}

// handleHistory reports the size of a git store's history; null for a
// synced folder, which keeps none.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	h, err := s.client.History()
	if errors.Is(err, store.ErrNoHistory) {
		ok(w, nil)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, h)
}

// handlePreview returns a session's last messages, from this machine's
// copy (from=local) or the store's.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	id := r.URL.Query().Get("id")
	if !session.ValidID(id) {
		fail(w, http.StatusBadRequest, fmt.Errorf("invalid session id"))
		return
	}
	const n = 6
	if r.URL.Query().Get("from") != "local" {
		msgs, err := s.client.Preview(id, n)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		ok(w, msgs)
		return
	}
	// Only a transcript ccsync lists itself is read: never a path from
	// the request.
	locals, err := session.ListAll()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	for _, l := range locals {
		if l.ID == id {
			msgs, err := session.PreviewFile(l.File, n)
			if err != nil {
				fail(w, http.StatusInternalServerError, err)
				return
			}
			ok(w, msgs)
			return
		}
	}
	fail(w, http.StatusNotFound, fmt.Errorf("no session %s on this machine", id))
}

// handleOpen opens the unlocked store's web page or folder. What to open
// comes from the store's registry entry, never from the request.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	s.progMu.Lock()
	link := s.link
	s.progMu.Unlock()
	if link == "" {
		fail(w, http.StatusBadRequest, errors.New("this store has no page to open"))
		return
	}
	browse(link)
	ok(w, nil)
}

// handleForget removes a store from this machine (not from its repo).
func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Store string `json:"store"`
	}
	if err := decode(r, &req); err != nil || req.Store == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("store is required"))
		return
	}
	if s.client != nil && s.client.Entry.Name == req.Store {
		s.client = nil
	}
	if err := s.stores.Forget(req.Store); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ok(w, nil)
}

// handleProgress reports the running operation's progress (zero: none).
func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	s.progMu.Lock()
	p := s.progress
	s.progMu.Unlock()
	ok(w, p)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := session.ListProjects()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if projects == nil {
		projects = []session.Project{} // JSON [] rather than null
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
		rows = append(rows, row{s, app.IsActive(s)})
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

// handleStatus relates every local session that is also in the store to
// its store copy: {id: "same"|"ahead"|"behind"|"diverged"|"unknown"},
// seen from this machine. Ids on one side only are absent.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	locals, err := session.ListAll()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	// The page abandons this request when it switches store; stop then.
	rels, err := s.client.Status(r.Context(), locals)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	out := make(map[string]string, len(rels))
	for id, rel := range rels {
		out[id] = rel.String()
	}
	ok(w, out)
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	var req struct {
		Slug          string   `json:"slug"`
		Project       string   `json:"project"` // display path; fallback cwd only
		IDs           []string `json:"ids"`
		IncludeActive bool     `json:"includeActive"` // push sessions modified in the last 30 s
		Overwrite     bool     `json:"overwrite"`     // replace newer, diverged or unreadable store copies
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
	res, err := s.client.Push(selected, req.Project, app.PushOptions{IncludeActive: req.IncludeActive, Overwrite: req.Overwrite})
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	type rejection struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Relation string `json:"relation"` // "behind", "diverged" or "unknown"
		Host     string `json:"host"`
	}
	rejected := make([]rejection, 0, len(res.Rejected))
	for _, r := range res.Rejected {
		rejected = append(rejected, rejection{r.Local.ID, r.Local.Title, r.Relation.String(), r.Store.Hostname})
	}
	ok(w, map[string]any{"pushed": len(res.Pushed), "skipped": len(res.Skipped), "rejected": rejected, "active": len(res.Active)})
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

// pullManyRequest selects sessions (none: all) and how their projects move.
type pullManyRequest struct {
	IDs   []string    `json:"ids"`
	Map   app.PathMap `json:"map"`
	Force bool        `json:"force"`
}

func (r *pullManyRequest) validate() error {
	for _, rule := range r.Map {
		if rule.From == "" || rule.To == "" {
			return fmt.Errorf("every path mapping needs both a from and a to")
		}
	}
	return nil
}

// handlePullPlan shows where each selected session would go, plus a
// suggested mapping for projects from another home directory.
func (s *Server) handlePullPlan(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	var req pullManyRequest
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	plan, err := s.client.PlanPull(req.IDs, req.Map)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	type row struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		From        string `json:"from"` // original project path
		Dest        string `json:"dest"`
		DestExists  bool   `json:"destExists"`
		LocalExists bool   `json:"localExists"`
	}
	rows := make([]row, 0, len(plan))
	var cwds []string
	for _, p := range plan {
		rows = append(rows, row{p.Meta.SessionID, p.Meta.Title, p.Meta.OriginalCwd, p.Dest, p.DestExists, p.LocalExists})
		cwds = append(cwds, p.Meta.OriginalCwd)
	}
	suggest := app.SuggestPathMap(cwds)
	if suggest == nil {
		suggest = app.PathMap{}
	}
	ok(w, map[string]any{"plan": rows, "suggest": suggest})
}

func (s *Server) handlePullMany(w http.ResponseWriter, r *http.Request) {
	if s.client == nil {
		fail(w, http.StatusForbidden, errLocked)
		return
	}
	var req pullManyRequest
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.client.PullMany(req.IDs, req.Map, req.Force)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	type failure struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Error string `json:"error"`
	}
	failed := make([]failure, 0, len(res.Failed))
	for _, f := range res.Failed {
		failed = append(failed, failure{f.ID, f.Title, f.Err.Error()})
	}
	ok(w, map[string]any{"pulled": len(res.Pulled), "unchanged": len(res.Unchanged), "failed": failed})
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
