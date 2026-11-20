package cli

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/vinh0809it/ccsync/internal/app"
	"github.com/vinh0809it/ccsync/internal/gui"
	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
)

func (c *cli) init(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	name := fs.String("name", "", "name for this store on this machine (default: from the repo or folder name)")
	ghRepo := fs.String("github", "", "create this private GitHub repo (`name` or owner/name) through the GitHub CLI")
	folder := fs.String("folder", "", "keep the store in this synced folder (Dropbox, iCloud Drive, ...) instead of git")
	allowPublic := fs.Bool("allow-public", false, "accept a public GitHub repo")
	if err := c.parseFlags(fs, args, "name", "github", "folder"); err != nil {
		return err
	}
	spec := app.NewStore{Name: *name, AllowPublic: *allowPublic}
	switch {
	case *folder != "" && *ghRepo == "" && fs.NArg() == 0:
		spec.Kind, spec.Location = store.KindFolder, *folder
	case *ghRepo != "" && *folder == "" && fs.NArg() == 0:
		spec.Kind, spec.CreateGitHubRepo = store.KindGit, *ghRepo
	case *ghRepo == "" && *folder == "" && fs.NArg() == 1:
		spec.Kind, spec.Location = store.KindGit, fs.Arg(0)
	default:
		return fmt.Errorf("%w: give one of: a repo URL, --github NAME, --folder DIR", errUsage)
	}
	if spec.Name == "" {
		spec.Name = nameFromURL(spec.Location + spec.CreateGitHubRepo)
	}
	if err := store.CheckName(spec.Name); err != nil {
		return fmt.Errorf("%w: %v (choose one with --name)", errUsage, err)
	}
	fmt.Fprintln(c.err, "Sessions are encrypted with your passphrase before they leave this machine.")
	fmt.Fprintln(c.err, "Every machine set up with this store and passphrase can push and pull them.")
	pass, err := c.passphrase(true)
	if err != nil {
		return err
	}
	cl, err := c.stores().Add(spec, pass)
	if errors.Is(err, store.ErrNeedsMigration) {
		fmt.Fprintf(c.out, "Joined store %q, made by ccsync 0.1.x; run: ccsync migrate\n", spec.Name)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Store %q (%s) is ready and selected.\nPush sessions with: ccsync push --all (from a project dir)\n", cl.Entry.Name, cl.Entry.Location())
	return nil
}

// nameFromURL derives a store name from a repo URL: ".../my-sessions.git"
// gives "my-sessions".
func nameFromURL(url string) string {
	base := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(base, "/:\\"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if len(name) > 32 {
		name = name[:32]
	}
	if store.CheckName(name) != nil {
		return "default"
	}
	return name
}

func (c *cli) storeCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch {
	case args[0] == "list" && len(args) == 1:
		reg, err := c.stores().List()
		if err != nil {
			return err
		}
		if len(reg.Stores) == 0 {
			return store.ErrNoStore
		}
		tw := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
		for _, e := range reg.Stores {
			mark := " "
			if e.Name == reg.Current {
				mark = "*"
			}
			fmt.Fprintf(tw, "%s %s\t%s\t%s\n", mark, e.Name, kindName(e.Kind), e.Location())
		}
		return tw.Flush()
	case args[0] == "use" && len(args) == 2:
		if err := c.stores().Use(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "Using store %q.\n", args[1])
		return nil
	case args[0] == "forget" && len(args) == 2:
		if err := c.stores().Forget(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "Forgot store %q on this machine. Its repo, other machines and your local sessions are untouched.\n", args[1])
		return nil
	}
	return fmt.Errorf("%w: unknown store command", errUsage)
}

func (c *cli) sessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	proj := projectFlag(fs)
	if err := c.parseFlags(fs, args, "project"); err != nil {
		return err
	}
	p, err := resolveProject(*proj)
	if err != nil {
		return err
	}
	sessions, err := session.ListForProject(p)
	if err != nil {
		return err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Mtime.After(sessions[j].Mtime) })
	for _, s := range sessions {
		live := ""
		if app.IsActive(s) {
			live = "  [active]"
		}
		fmt.Fprintf(c.out, "%s  %s  %7s  %s%s\n",
			s.ID, s.Mtime.Format("2006-01-02 15:04"), humanSize(s.Size), title(s.Title), live)
	}
	return nil
}

func (c *cli) push(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	proj := projectFlag(fs)
	all := fs.Bool("all", false, "push every session of the project")
	allProjects := fs.Bool("all-projects", false, "push every session of every project")
	force := fs.Bool("force", false, "overwrite store copies that are newer, diverged or unreadable")
	includeActive := fs.Bool("include-active", false, "also push sessions modified in the last 30 seconds")
	if err := c.parseFlags(fs, args, "project"); err != nil {
		return err
	}
	modes := 0
	for _, on := range []bool{*all, *allProjects, fs.NArg() > 0} {
		if on {
			modes++
		}
	}
	if modes != 1 {
		return fmt.Errorf("%w: pass session ids (see: ccsync sessions), --all or --all-projects", errUsage)
	}
	if err := c.requireStore(); err != nil {
		return err
	}

	var p string
	var selected []session.Session
	var err error
	if *allProjects {
		selected, err = session.ListAll()
	} else {
		if p, err = resolveProject(*proj); err != nil {
			return err
		}
		selected, err = session.ListForProject(p)
		if err == nil && !*all {
			selected, err = selectSessions(selected, fs.Args())
		}
	}
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return fmt.Errorf("nothing to push")
	}

	client, err := c.unlock()
	if err != nil {
		return err
	}
	res, err := client.Push(selected, p, app.PushOptions{IncludeActive: *includeActive, Overwrite: *force})
	for _, m := range res.Pushed {
		fmt.Fprintf(c.out, "pushed   %s  %s\n", m.SessionID, title(m.Title))
	}
	for _, r := range res.Rejected {
		fmt.Fprintf(c.out, "rejected %s  %s — %s\n", r.Local.ID, title(r.Local.Title), rejectionReason(r))
	}
	for _, s := range res.Active {
		fmt.Fprintf(c.out, "skipped  %s  %s — active in the last %s (--include-active to push it)\n", s.ID, title(s.Title), app.ActiveGrace)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "pushed %d, already up to date %d, rejected %d, active %d.\n",
		len(res.Pushed), len(res.Skipped), len(res.Rejected), len(res.Active))
	if len(res.Rejected) > 0 {
		return fmt.Errorf("%d session(s) not pushed: pull them first, or push with --force to overwrite the store copy (a git store keeps it in its history)", len(res.Rejected))
	}
	if len(res.Active) > 0 && fs.NArg() > 0 {
		return fmt.Errorf("%d session(s) you named are active and were not pushed", len(res.Active))
	}
	return nil
}

func rejectionReason(r app.Rejection) string {
	from := fmt.Sprintf("pushed from %s %s", r.Store.Hostname, r.Store.PushedAt.Local().Format("2006-01-02 15:04"))
	switch r.Relation {
	case session.Behind:
		return "the store copy is newer (" + from + ")"
	case session.Diverged:
		return "it was also continued elsewhere (" + from + ")"
	default:
		return "the copies can't be compared (" + from + ")"
	}
}

// selectSessions resolves id prefixes against local sessions.
func selectSessions(locals []session.Session, prefixes []string) ([]session.Session, error) {
	seen := map[string]bool{}
	var selected []session.Session
	for _, want := range prefixes {
		if want == "" {
			return nil, fmt.Errorf("empty session id")
		}
		var match *session.Session
		for i := range locals {
			if strings.HasPrefix(locals[i].ID, want) {
				if match != nil {
					return nil, fmt.Errorf("session id %q is ambiguous (%s, %s, ...)", want, match.ID, locals[i].ID)
				}
				match = &locals[i]
			}
		}
		if match == nil {
			return nil, fmt.Errorf("no local session matches %q", want)
		}
		if !seen[match.ID] {
			seen[match.ID] = true
			selected = append(selected, *match)
		}
	}
	return selected, nil
}

func (c *cli) ls(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	if err := c.parseFlags(fs, args); err != nil {
		return err
	}
	client, err := c.unlock()
	if err != nil {
		return err
	}
	metas, unreadable, err := client.List()
	if err != nil {
		return err
	}
	for _, id := range unreadable {
		fmt.Fprintf(c.err, "warning: can't read %s (pushed by an outdated ccsync? run: ccsync migrate)\n", id)
	}
	if len(metas) == 0 {
		fmt.Fprintln(c.out, "store is empty")
		return nil
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].SessionMtime.After(metas[j].SessionMtime) })
	for _, m := range metas {
		fmt.Fprintf(c.out, "%s  %s  %7s  %-20s  %s  (from %s)\n",
			m.SessionID, m.SessionMtime.Format("2006-01-02 15:04"), humanSize(m.Size),
			m.ProjectLabel, title(m.Title), m.Hostname)
	}
	return nil
}

func (c *cli) pull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	to := fs.String("to", "", "project directory to install one session for")
	all := fs.Bool("all", false, "pull every session in the store")
	var maps mapFlags
	fs.Var(&maps, "map", "move projects: FROM=TO (repeatable), e.g. /home/alice/dev=/Users/alice/code")
	keep := fs.Bool("keep-paths", false, "install at the original project paths even if they don't exist here")
	dryRun := fs.Bool("dry-run", false, "only show where each session would go")
	force := fs.Bool("force", false, "replace local copies that are newer, diverged or open in Claude Code (kept as .bak files)")
	if err := c.parseFlags(fs, args, "to", "map"); err != nil {
		return err
	}
	switch {
	case *to != "" && (fs.NArg() != 1 || *all || len(maps) > 0):
		return fmt.Errorf("%w: --to takes exactly one session id (and no --all or --map)", errUsage)
	case *to == "" && *all == (fs.NArg() > 0):
		return fmt.Errorf("%w: pass session ids, or --all", errUsage)
	}
	client, err := c.unlock()
	if err != nil {
		return err
	}
	if *to != "" {
		return c.pullOne(client, fs.Arg(0), *to, *force)
	}

	plan, err := client.PlanPull(fs.Args(), app.PathMap(maps))
	if err != nil {
		return err
	}
	if len(maps) == 0 && !*keep && !*dryRun {
		// Projects from another home directory would land in folders that
		// don't exist here: ask for a mapping instead of guessing.
		var cwds []string
		for _, p := range plan {
			cwds = append(cwds, p.Meta.OriginalCwd)
		}
		if suggest := app.SuggestPathMap(cwds); len(suggest) > 0 {
			var flags []string
			for _, r := range suggest {
				flags = append(flags, fmt.Sprintf("--map %s=%s", shellArg(r.From), shellArg(r.To)))
			}
			return fmt.Errorf("these sessions come from projects under %s, which doesn't exist here — say where they go, e.g.:\n  ccsync pull %s %s\n(--dry-run shows the plan; --keep-paths keeps the original paths)",
				suggest[0].From, strings.Join(pullTargets(*all, fs.Args()), " "), strings.Join(flags, " "))
		}
	}
	if *dryRun {
		for _, p := range plan {
			note := ""
			switch {
			case p.Dest == "":
				note = "  (no project path recorded: pull it on its own with --to)"
			case !p.DestExists:
				note = "  (folder doesn't exist here)"
			case p.LocalExists:
				note = "  (already here: will be compared)"
			}
			fmt.Fprintf(c.out, "%s  %-28s -> %s%s\n", p.Meta.SessionID, title(p.Meta.Title), p.Dest, note)
		}
		return nil
	}

	res, err := client.PullMany(fs.Args(), app.PathMap(maps), *force)
	if err != nil {
		return err
	}
	for _, r := range res.Pulled {
		fmt.Fprintf(c.out, "pulled   %s  %-28s -> %s\n", r.Meta.SessionID, title(r.Meta.Title), r.Dest)
	}
	for _, f := range res.Failed {
		fmt.Fprintf(c.out, "failed   %s  %-28s — %v\n", f.ID, title(f.Title), f.Err)
	}
	fmt.Fprintf(c.out, "pulled %d, already up to date %d, failed %d.\n", len(res.Pulled), len(res.Unchanged), len(res.Failed))
	if len(res.Failed) > 0 {
		return fmt.Errorf("%d session(s) not pulled", len(res.Failed))
	}
	return nil
}

func (c *cli) pullOne(client *app.Client, id, to string, force bool) error {
	res, err := client.Pull(id, to, force)
	if err != nil {
		return err
	}
	switch {
	case res.Unchanged:
		fmt.Fprintf(c.out, "already up to date: %s\n", res.Target)
	case res.Updated:
		fmt.Fprintf(c.out, "updated %s\n", res.Target)
	case res.Backup != "":
		fmt.Fprintf(c.out, "installed %s\n(previous local copy kept as %s)\n", res.Target, res.Backup)
	default:
		fmt.Fprintf(c.out, "installed %s\n", res.Target)
	}
	fmt.Fprintf(c.out, "resume with:\n  %s\n", res.ResumeCommand())
	return nil
}

// pullTargets echoes what was asked for, for a suggested command line.
func pullTargets(all bool, ids []string) []string {
	if all {
		return []string{"--all"}
	}
	return ids
}

// mapFlags collects repeated --map FROM=TO flags.
type mapFlags app.PathMap

func (m *mapFlags) String() string { return fmt.Sprint(*m) }

func (m *mapFlags) Set(s string) error {
	r, err := app.ParseRule(s)
	if err != nil {
		return err
	}
	*m = append(*m, r)
	return nil
}

// shellArg quotes s for a POSIX shell when needed.
func shellArg(s string) string {
	if strings.ContainsAny(s, " '\"$`\\") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

func (c *cli) rm(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	if err := c.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: expected session ids", errUsage)
	}
	client, err := c.unlock()
	if err != nil {
		return err
	}
	removed, err := client.Remove(fs.Args())
	if err != nil {
		return err
	}
	for _, m := range removed {
		fmt.Fprintf(c.out, "removed %s (%s)\n", m.SessionID, title(m.Title))
	}
	fmt.Fprintln(c.out, "Local copies are untouched; old versions remain in the store's git history.")
	return nil
}

func (c *cli) compact(args []string) error {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "confirm: old versions can't be recovered afterwards")
	if err := c.parseFlags(fs, args); err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintln(c.err, "compact replaces the store's git history with one commit of its current")
		fmt.Fprintln(c.err, "sessions. Replaced and deleted versions are gone for good; other machines")
		fmt.Fprintln(c.err, "pick up the new history on their next command.")
		return fmt.Errorf("%w: run again with --yes to go ahead", errUsage)
	}
	client, err := c.unlock()
	if err != nil {
		return err
	}
	r, err := client.Compact()
	if err != nil {
		return err
	}
	if r.Commits <= 1 {
		fmt.Fprintln(c.out, "The store's history is already a single commit.")
		return nil
	}
	if r.Before < 0 {
		fmt.Fprintf(c.out, "Compacted %d commits into 1.\n", r.Commits)
	} else {
		fmt.Fprintf(c.out, "Compacted %d commits into 1; this clone's history went from %s to %s.\n",
			r.Commits, humanSize(r.Before), humanSize(r.After))
	}
	fmt.Fprintln(c.out, "The git host may take a while to reclaim the space of the old history.")
	return nil
}

func (c *cli) migrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	if err := c.parseFlags(fs, args); err != nil {
		return err
	}
	if err := c.requireStore(); err != nil {
		return err
	}
	fmt.Fprintln(c.err, "NOTE: after migrating, ccsync 0.1.x can no longer read this store —")
	fmt.Fprintln(c.err, "      upgrade ccsync on every machine.")
	pass, err := c.passphrase(false)
	if err != nil {
		return err
	}
	n, err := c.stores().Migrate(c.store, pass)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "store is at format %d (%d session(s) converted).\n", store.Format, n)
	return nil
}

func (c *cli) gui(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ContinueOnError)
	port := fs.Int("port", 0, "port to listen on (default: random free port)")
	noOpen := fs.Bool("no-open", false, "don't open the browser automatically")
	browser := fs.Bool("browser", false, "use the browser even in a desktop build")
	if err := c.parseFlags(fs, args, "port"); err != nil {
		return err
	}
	if gui.DesktopSupported && !*browser && !*noOpen {
		return gui.RunWindow(*port)
	}
	return gui.Run(*port, !*noOpen)
}

func (c *cli) version(args []string) error {
	fmt.Fprintln(c.out, "ccsync", c.ver)
	return nil
}

func projectFlag(fs *flag.FlagSet) *string {
	return fs.String("project", "", "project directory (default: current directory)")
}

func resolveProject(p string) (string, error) {
	if p == "" {
		p = "."
	}
	return session.ResolveProject(p)
}

func kindName(k store.Kind) string {
	if k == "" {
		return string(store.KindGit)
	}
	return string(k)
}
