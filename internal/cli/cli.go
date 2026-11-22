// Package cli implements the ccsync command line.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errUsage marks errors caused by how the command was invoked.
var errUsage = errors.New("usage")

type command struct {
	name    string
	args    string
	summary string
	run     func(c *cli, args []string) error
}

// commands is the command table; usage is generated from it.
var commands = []command{
	{"init", "<repo-url> | --github NAME | --folder DIR  [--name NAME] [--account GITHUB-USER]", "set up a store on this machine (created if empty)", (*cli).init},
	{"store", "list | use NAME | forget NAME", "manage this machine's stores", (*cli).storeCmd},
	{"sessions", "[--project DIR]", "list local sessions of a project", (*cli).sessions},
	{"push", "<ids...> | --all | --all-projects [--force] [--include-active] [--project DIR]", "encrypt and push sessions to the store", (*cli).push},
	{"ls", "", "list sessions in the store", (*cli).ls},
	{"pull", "<id> --to DIR | <ids...> | --all  [--map FROM=TO]... [--dry-run] [--force]", "install sessions here (all: at their projects, moved by --map)", (*cli).pull},
	{"rm", "<id>...", "delete sessions from the store", (*cli).rm},
	{"compact", "--yes", "drop a git store's history to free space (old versions are lost)", (*cli).compact},
	{"gui", "[--port N] [--no-open] [--browser]", "open the GUI (native window in desktop builds)", (*cli).gui},
	{"migrate", "", "upgrade a store made by ccsync 0.1.x", (*cli).migrate},
	{"version", "", "print the version", (*cli).version},
}

type cli struct {
	out, err io.Writer
	ver      string
	store    string // store to use (--store or $CCSYNC_STORE); empty: the current one
}

// Run executes the command line args (without the program name) and
// returns the process exit code. buildVersion is the version stamped at
// build time, if any.
func Run(args []string, buildVersion string) int {
	c := &cli{out: os.Stdout, err: os.Stderr, ver: resolveVersion(buildVersion), store: os.Getenv("CCSYNC_STORE")}
	return c.run(args)
}

func (c *cli) run(args []string) int {
	// Global option: --store NAME (or --store=NAME) before the command.
	if len(args) > 0 {
		if v, ok := strings.CutPrefix(args[0], "--store="); ok {
			c.store, args = v, args[1:]
		} else if args[0] == "--store" {
			if len(args) < 2 {
				fmt.Fprintln(c.err, "--store needs a store name")
				return exitUsage
			}
			c.store, args = args[1], args[2:]
		}
	}
	if len(args) == 0 {
		// Double-clicked (or run bare): open the GUI.
		fmt.Fprintln(c.err, "Opening the ccsync GUI — run `ccsync help` for the commands.")
		args = []string{"gui"}
	}
	name := args[0]
	switch name {
	case "help", "-h", "--help":
		c.usage(c.out)
		return exitOK
	case "-v", "--version":
		name = "version"
	}
	for _, cmd := range commands {
		if cmd.name != name {
			continue
		}
		err := cmd.run(c, args[1:])
		switch {
		case err == nil:
			return exitOK
		case errors.Is(err, errUsage):
			fmt.Fprintf(c.err, "%v\nusage: ccsync %s %s\n", err, cmd.name, cmd.args)
			return exitUsage
		default:
			fmt.Fprintln(c.err, "error:", err)
			return exitError
		}
	}
	fmt.Fprintf(c.err, "unknown command %q\n\n", name)
	c.usage(c.err)
	return exitUsage
}

func (c *cli) usage(w io.Writer) {
	fmt.Fprint(w, "ccsync — sync Claude Code sessions between machines (encrypted git store)\n\nUsage: ccsync [--store NAME] <command>   (no command: open the GUI)\n\n")
	for _, cmd := range commands {
		fmt.Fprintf(w, "  ccsync %-10s %s\n  %12s %s\n", cmd.name, cmd.args, "", cmd.summary)
	}
	fmt.Fprint(w, `
Session ids may be abbreviated to any unique prefix. Commands use the
current store unless --store (or $CCSYNC_STORE) names another.
The passphrase is asked interactively (or read from $CCSYNC_PASSPHRASE).
Everything in the repo is encrypted; git credentials are never touched —
pushing and pulling use your existing git auth (SSH key or credential helper).
`)
}

// resolveVersion prefers the stamped version, then the module version
// recorded by `go install module@version`.
func resolveVersion(stamped string) string {
	if stamped != "" {
		return stamped
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// parseFlags parses args, allowing flags after positional arguments
// (`ccsync push <id> --force`). valueFlags names the flags that take a
// value, so their value isn't mistaken for a positional argument.
func (c *cli) parseFlags(fs *flag.FlagSet, args []string, valueFlags ...string) error {
	fs.SetOutput(c.err)
	if err := fs.Parse(reorderArgs(args, valueFlags...)); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	return nil
}

// reorderArgs moves flags ahead of positionals; Go's flag package
// otherwise stops parsing at the first positional. A "--" ends flags.
func reorderArgs(args []string, valueFlags ...string) []string {
	takesValue := map[string]bool{}
	for _, f := range valueFlags {
		takesValue[f] = true
	}
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positionals = append(positionals, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if takesValue[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			positionals = append(positionals, a)
		}
	}
	return append(append(flags, "--"), positionals...)
}
