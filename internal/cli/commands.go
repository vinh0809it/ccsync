package cli

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vinh-vh/ccsync/internal/app"
	"github.com/vinh-vh/ccsync/internal/gui"
	"github.com/vinh-vh/ccsync/internal/session"
	"github.com/vinh-vh/ccsync/internal/store"
)

func (c *cli) init(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	name := fs.String("name", "", "name for this store on this machine (default: from the repo name)")
	if err := c.parseFlags(fs, args, "name"); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected one repo URL", errUsage)
	}
	url := fs.Arg(0)
	if *name == "" {
		*name = nameFromURL(url)
	}
	if err := store.CheckName(*name); err != nil {
		return fmt.Errorf("%w: %v (choose one with --name)", errUsage, err)
	}
	fmt.Fprintln(c.err, "NOTE: everything pushed is encrypted with your passphrase.")
	fmt.Fprintln(c.err, "      A private repo is still recommended; with a public repo,")
	fmt.Fprintln(c.err, "      anyone can see THAT you sync and WHEN, but not the content.")
	pass, err := c.passphrase(true)
	if err != nil {
		return err
	}
	if _, err := c.stores().Add(*name, url, pass); err != nil {
		if errors.Is(err, store.ErrNeedsMigration) {
			fmt.Fprintf(c.out, "Joined store %q, made by ccsync 0.1.x; run: ccsync migrate\n", *name)
			return nil
		}
		return err
	}
	fmt.Fprintf(c.out, "Store %q is ready and selected. Push sessions with: ccsync push --all (from a project dir)\n", *name)
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
		for _, e := range reg.Stores {
			mark := " "
			if e.Name == reg.Current {
				mark = "*"
			}
			fmt.Fprintf(c.out, "%s %-20s %s\n", mark, e.Name, e.RepoURL)
		}
		return nil
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
		if time.Since(s.Mtime) < app.ActiveGrace {
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
	force := fs.Bool("force", false, "include active sessions and overwrite store copies that are newer or diverged")
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
	res, err := client.Push(selected, p, app.PushOptions{IncludeActive: *force, Overwrite: *force})
	for _, m := range res.Pushed {
		fmt.Fprintf(c.out, "pushed   %s  %s\n", m.SessionID, title(m.Title))
	}
	for _, r := range res.Rejected {
		fmt.Fprintf(c.out, "rejected %s  %s — %s\n", r.Local.ID, title(r.Local.Title), rejectionReason(r))
	}
	for _, s := range res.Active {
		fmt.Fprintf(c.out, "skipped  %s  %s — active in the last %s (--force to include)\n", s.ID, title(s.Title), app.ActiveGrace)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "pushed %d, already up to date %d, rejected %d, active %d.\n",
		len(res.Pushed), len(res.Skipped), len(res.Rejected), len(res.Active))
	if len(res.Rejected) > 0 {
		return fmt.Errorf("%d session(s) not pushed: pull them first, or push with --force to overwrite the store copy (it stays in the store's git history)", len(res.Rejected))
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
	to := fs.String("to", "", "project directory to install the session for (required)")
	force := fs.Bool("force", false, "replace a local copy that is newer or diverged (kept as a .bak file)")
	if err := c.parseFlags(fs, args, "to"); err != nil {
		return err
	}
	if fs.NArg() != 1 || *to == "" {
		return fmt.Errorf("%w: expected one session id and --to", errUsage)
	}
	client, err := c.unlock()
	if err != nil {
		return err
	}
	res, err := client.Pull(fs.Arg(0), *to, *force)
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
