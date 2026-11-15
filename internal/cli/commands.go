package cli

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vinhvh0809/ccsync/internal/app"
	"github.com/vinhvh0809/ccsync/internal/gui"
	"github.com/vinhvh0809/ccsync/internal/session"
	"github.com/vinhvh0809/ccsync/internal/store"
)

func (c *cli) init(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	if err := c.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected one repo URL", errUsage)
	}
	fmt.Fprintln(c.err, "NOTE: everything pushed is encrypted with your passphrase.")
	fmt.Fprintln(c.err, "      A private repo is still recommended; with a public repo,")
	fmt.Fprintln(c.err, "      anyone can see THAT you sync and WHEN, but not the content.")
	pass, err := c.passphrase(true)
	if err != nil {
		return err
	}
	st, err := store.Open()
	if err != nil {
		return err
	}
	if _, err := app.Setup(st, fs.Arg(0), pass); err != nil {
		if errors.Is(err, store.ErrNeedsMigration) {
			fmt.Fprintln(c.out, "Joined a store made by ccsync 0.1.x; run: ccsync migrate")
			return nil
		}
		return err
	}
	fmt.Fprintln(c.out, "ccsync is ready. Push sessions with: ccsync push --all (from a project dir)")
	return nil
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
	force := fs.Bool("force", false, "include sessions that look active")
	if err := c.parseFlags(fs, args, "project"); err != nil {
		return err
	}
	if *all == (fs.NArg() > 0) {
		return fmt.Errorf("%w: pass session ids (see: ccsync sessions) or --all", errUsage)
	}
	p, err := resolveProject(*proj)
	if err != nil {
		return err
	}
	if _, err := store.LoadConfig(); err != nil {
		return err
	}
	locals, err := session.ListForProject(p)
	if err != nil {
		return err
	}

	var selected []session.Session
	if *all {
		for _, s := range locals {
			if time.Since(s.Mtime) < app.ActiveGrace && !*force {
				fmt.Fprintf(c.err, "skipping %s (active session; --force to include)\n", s.ID)
				continue
			}
			selected = append(selected, s)
		}
	} else if selected, err = selectSessions(locals, fs.Args()); err != nil {
		return err
	}
	if len(selected) == 0 {
		return fmt.Errorf("nothing to push")
	}

	client, err := c.unlock()
	if err != nil {
		return err
	}
	res, err := client.Push(selected, p, *force)
	if err != nil {
		return err
	}
	for _, m := range res.Pushed {
		fmt.Fprintf(c.out, "pushed %s (%s)\n", m.SessionID, title(m.Title))
	}
	for _, id := range res.Skipped {
		fmt.Fprintf(c.out, "skipped %s (already up to date in the store)\n", id)
	}
	fmt.Fprintf(c.out, "pushed %d, skipped %d.\n", len(res.Pushed), len(res.Skipped))
	return nil
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
	force := fs.Bool("force", false, "replace a different local copy (kept as a .bak file)")
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
	if _, err := store.LoadConfig(); err != nil {
		return err
	}
	fmt.Fprintln(c.err, "NOTE: after migrating, ccsync 0.1.x can no longer read this store —")
	fmt.Fprintln(c.err, "      upgrade ccsync on every machine.")
	pass, err := c.passphrase(false)
	if err != nil {
		return err
	}
	st, err := store.Open()
	if err != nil {
		return err
	}
	n, err := app.Migrate(st, pass)
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
