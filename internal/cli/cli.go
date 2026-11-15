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
	{"init", "<git-repo-url>", "set up this machine against a store repo", (*cli).init},
	{"sessions", "[--project DIR]", "list local sessions of a project", (*cli).sessions},
	{"push", "[ids...] [--all] [--force] [--project DIR]", "encrypt and push sessions to the store", (*cli).push},
	{"ls", "", "list sessions in the store", (*cli).ls},
	{"pull", "<id> --to DIR [--force]", "install a session for project DIR", (*cli).pull},
	{"rm", "<id>...", "delete sessions from the store", (*cli).rm},
	{"gui", "[--port N] [--no-open] [--browser]", "open the GUI (native window in desktop builds)", (*cli).gui},
	{"migrate", "", "upgrade a store made by ccsync 0.1.x", (*cli).migrate},
	{"version", "", "print the version", (*cli).version},
}

type cli struct {
	out, err io.Writer
	ver      string
}

// Run executes the command line args (without the program name) and
// returns the process exit code. buildVersion is the version stamped at
// build time, if any.
func Run(args []string, buildVersion string) int {
	c := &cli{out: os.Stdout, err: os.Stderr, ver: resolveVersion(buildVersion)}
	return c.run(args)
}

func (c *cli) run(args []string) int {
	if len(args) == 0 {
		c.usage(c.err)
		return exitUsage
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
	fmt.Fprint(w, "ccsync — sync Claude Code sessions between machines (encrypted git store)\n\nUsage:\n")
	for _, cmd := range commands {
		fmt.Fprintf(w, "  ccsync %-10s %s\n  %12s %s\n", cmd.name, cmd.args, "", cmd.summary)
	}
	fmt.Fprint(w, `
Session ids may be abbreviated to any unique prefix.
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
