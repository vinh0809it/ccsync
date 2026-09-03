// ccsync — sync Claude Code sessions between machines through an
// end-to-end-encrypted git repository.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"ccsync/internal/app"
	"ccsync/internal/gui"
	"ccsync/internal/session"
	"ccsync/internal/store"
)


func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "push":
		err = cmdPush(os.Args[2:])
	case "ls":
		err = cmdLs(os.Args[2:])
	case "pull":
		err = cmdPull(os.Args[2:])
	case "sessions":
		err = cmdSessions(os.Args[2:])
	case "gui":
		err = cmdGui(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`ccsync — sync Claude Code sessions between machines (encrypted git store)

Usage:
  ccsync init <git-repo-url>        set up this machine against a store repo
  ccsync sessions [--project DIR]   list local sessions of a project
  ccsync push [ids...] [--project DIR] [--all] [--force]
                                    encrypt & push sessions to the store
  ccsync ls                         list sessions available in the store
  ccsync pull <id> --to DIR [--force]
                                    install a session for project DIR
                                    (then: cd DIR && claude --resume <id>)
  ccsync gui [--port N] [--no-open] [--browser]
                                    GUI (native window in desktop builds,
                                    otherwise local web page; setup/push/pull)

The passphrase is asked interactively (or read from $CCSYNC_PASSPHRASE).
Everything in the repo is encrypted; git credentials are never touched —
pushing/pulling uses your existing git auth (SSH key or credential helper).
`)
}

func passphrase(confirm bool) (string, error) {
	if p := os.Getenv("CCSYNC_PASSPHRASE"); p != "" {
		return p, nil
	}
	fmt.Fprint(os.Stderr, "Store passphrase: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", fmt.Errorf("empty passphrase")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Confirm passphrase: ")
		b2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b) != string(b2) {
			return "", fmt.Errorf("passphrases do not match")
		}
	}
	return string(b), nil
}

// reorderArgs moves flags ahead of positionals so `ccsync push <id> --project DIR`
// works; Go's flag package otherwise stops parsing at the first positional.
// valueFlags names the flags that consume a following argument.
func reorderArgs(args []string, valueFlags ...string) []string {
	takesValue := map[string]bool{}
	for _, f := range valueFlags {
		takesValue[f] = true
	}
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if takesValue[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positionals = append(positionals, a)
		}
	}
	return append(flags, positionals...)
}

func projectFlag(fs *flag.FlagSet) *string {
	return fs.String("project", "", "project directory (default: current directory)")
}

func resolveProject(p string) (string, error) {
	if p == "" {
		return os.Getwd()
	}
	return filepath.Abs(p)
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ccsync init <git-repo-url>")
	}
	url := fs.Arg(0)
	fmt.Fprintln(os.Stderr, "NOTE: everything pushed is encrypted with your passphrase.")
	fmt.Fprintln(os.Stderr, "      A private repo is still recommended; with a public repo,")
	fmt.Fprintln(os.Stderr, "      anyone can see THAT you sync and WHEN, but not the content.")
	pass, err := passphrase(true)
	if err != nil {
		return err
	}
	if err := store.Init(url, pass); err != nil {
		return err
	}
	fmt.Println("ccsync is ready. Push sessions with: ccsync push --all (from a project dir)")
	return nil
}

func cmdSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	proj := projectFlag(fs)
	fs.Parse(args)
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
		fmt.Printf("%s  %s  %7s  %s%s\n",
			s.ID, s.Mtime.Format("2006-01-02 15:04"), humanSize(s.Size), title(s.Title), live)
	}
	return nil
}

func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	proj := projectFlag(fs)
	all := fs.Bool("all", false, "push every session of the project")
	force := fs.Bool("force", false, "include sessions that look active")
	fs.Parse(reorderArgs(args, "project"))

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
				fmt.Fprintf(os.Stderr, "skipping %s (active session; --force to include)\n", s.ID)
				continue
			}
			selected = append(selected, s)
		}
	} else {
		if fs.NArg() == 0 {
			return fmt.Errorf("pass session ids (see: ccsync sessions) or --all")
		}
		seen := map[string]bool{}
		for _, want := range fs.Args() {
			if want == "" {
				return fmt.Errorf("empty session id")
			}
			var match *session.Session
			for i := range locals {
				if strings.HasPrefix(locals[i].ID, want) {
					if match != nil {
						return fmt.Errorf("session id %q is ambiguous (%s, %s, ...)", want, match.ID, locals[i].ID)
					}
					match = &locals[i]
				}
			}
			if match == nil {
				return fmt.Errorf("no local session matches %q", want)
			}
			if !seen[match.ID] {
				seen[match.ID] = true
				selected = append(selected, *match)
			}
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("nothing to push")
	}

	pass, err := passphrase(false)
	if err != nil {
		return err
	}
	pushed, skipped, err := app.Push(pass, selected, p, *force)
	if err != nil {
		return err
	}
	for _, m := range pushed {
		fmt.Printf("pushed %s (%s)\n", m.SessionID, title(m.Title))
	}
	for _, id := range skipped {
		fmt.Printf("skipped %s (already up to date in the store)\n", id)
	}
	fmt.Printf("pushed %d, skipped %d.\n", len(pushed), len(skipped))
	return nil
}

func cmdLs(args []string) error {
	if _, err := store.LoadConfig(); err != nil {
		return err
	}
	pass, err := passphrase(false)
	if err != nil {
		return err
	}
	if err := store.Sync(); err != nil {
		return err
	}
	if err := store.VerifyPassphrase(pass); err != nil {
		return err
	}
	metas, err := store.List(pass)
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Println("store is empty")
		return nil
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].SessionMtime.After(metas[j].SessionMtime) })
	for _, m := range metas {
		fmt.Printf("%s  %s  %7s  %-20s  %s  (from %s)\n",
			m.SessionID, m.SessionMtime.Format("2006-01-02 15:04"), humanSize(m.Size),
			m.ProjectLabel, title(m.Title), m.Hostname)
	}
	return nil
}

func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	to := fs.String("to", "", "project directory to install the session for (required)")
	force := fs.Bool("force", false, "overwrite an existing local session file")
	fs.Parse(reorderArgs(args, "to"))
	if fs.NArg() != 1 || *to == "" {
		return fmt.Errorf("usage: ccsync pull <session-id> --to <project-dir>")
	}
	if _, err := store.LoadConfig(); err != nil {
		return err
	}
	pass, err := passphrase(false)
	if err != nil {
		return err
	}
	meta, target, err := app.Pull(pass, fs.Arg(0), *to, *force)
	if err != nil {
		return err
	}
	dest, _ := filepath.Abs(*to)
	fmt.Printf("installed %s\n", target)
	fmt.Printf("resume with:\n  cd %s && claude --resume %s\n", dest, meta.SessionID)
	return nil
}

func cmdGui(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ExitOnError)
	port := fs.Int("port", 0, "port to listen on (default: random free port)")
	noOpen := fs.Bool("no-open", false, "don't open the browser automatically")
	browser := fs.Bool("browser", false, "use the browser even in a desktop build")
	fs.Parse(reorderArgs(args, "port"))
	if gui.DesktopSupported && !*browser && !*noOpen {
		return gui.RunWindow(*port)
	}
	return gui.Run(*port, !*noOpen)
}

func title(t string) string {
	if t == "" {
		return "(untitled)"
	}
	return t
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
