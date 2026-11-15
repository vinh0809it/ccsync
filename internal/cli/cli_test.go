package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vinh-vh/ccsync/internal/session"
	"github.com/vinh-vh/ccsync/internal/testutil"
)

// run executes a command line and returns exit code, stdout and stderr.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{out: &out, err: &errOut, ver: "test"}
	return c.run(args), out.String(), errOut.String()
}

func TestUsageAndExitCodes(t *testing.T) {
	if code, out, _ := run(t, "--help"); code != exitOK || !strings.Contains(out, "ccsync rm") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
	if code, _, _ := run(t); code != exitUsage {
		t.Fatalf("no args: code=%d", code)
	}
	if code, _, errOut := run(t, "frobnicate"); code != exitUsage || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("unknown: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := run(t, "pull", "abc"); code != exitUsage || !strings.Contains(errOut, "usage: ccsync pull") {
		t.Fatalf("bad pull args: code=%d err=%q", code, errOut)
	}
	if code, out, _ := run(t, "--version"); code != exitOK || out != "ccsync test\n" {
		t.Fatalf("version: code=%d out=%q", code, out)
	}
}

func TestReorderArgs(t *testing.T) {
	got := reorderArgs([]string{"abc", "--project", "/p", "--force", "def", "--to=/x", "--", "-weird"}, "project")
	want := []string{"--project", "/p", "--force", "--to=/x", "--", "abc", "def", "-weird"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestSelectSessions(t *testing.T) {
	locals := []session.Session{{ID: "aaaa1111"}, {ID: "aaaa2222"}, {ID: "bbbb0000"}}
	if got, err := selectSessions(locals, []string{"bbbb", "bbbb0000"}); err != nil || len(got) != 1 {
		t.Fatalf("duplicate prefixes: %v %v", got, err)
	}
	for _, bad := range []string{"aaaa", "cccc", ""} {
		if _, err := selectSessions(locals, []string{bad}); err == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
}

// TestCommandsEndToEnd runs the CLI the way a user does across two
// machines: init, push, ls, pull, rm.
func TestCommandsEndToEnd(t *testing.T) {
	t.Setenv("CCSYNC_PASSPHRASE", "cli-test-pass")
	remote := testutil.Remote(t)
	project := filepath.Join(t.TempDir(), "webapp")
	os.MkdirAll(project, 0o755)
	project, _ = session.ResolveProject(project)
	id := "cccccccc-aaaa-bbbb-cccc-000000000001"

	testutil.NewMachine(t)
	mustRun(t, "init", remote)
	testutil.WriteSession(t, os.Getenv("CLAUDE_CONFIG_DIR"), session.SlugForPath(project), id,
		testutil.TitleLine("cli-flow"), testutil.CwdLine(project))
	if out := mustRun(t, "sessions", "--project", project); !strings.Contains(out, "cli-flow") {
		t.Fatalf("sessions: %q", out)
	}
	if out := mustRun(t, "push", "cccc", "--project", project); !strings.Contains(out, "pushed 1, skipped 0") {
		t.Fatalf("push: %q", out)
	}

	testutil.NewMachine(t)
	mustRun(t, "init", remote)
	if out := mustRun(t, "ls"); !strings.Contains(out, id) || !strings.Contains(out, "cli-flow") {
		t.Fatalf("ls: %q", out)
	}
	dest := filepath.Join(t.TempDir(), "elsewhere")
	if out := mustRun(t, "pull", "cccc", "--to", dest); !strings.Contains(out, "claude --resume "+id) {
		t.Fatalf("pull: %q", out)
	}
	if out := mustRun(t, "rm", id); !strings.Contains(out, "removed "+id) {
		t.Fatalf("rm: %q", out)
	}
	if out := mustRun(t, "ls"); !strings.Contains(out, "store is empty") {
		t.Fatalf("ls after rm: %q", out)
	}
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := run(t, args...)
	if code != exitOK {
		t.Fatalf("ccsync %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}
