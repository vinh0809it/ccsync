// Package testutil holds helpers shared by ccsync's tests: throwaway git
// remotes, simulated machines and Claude Code session fixtures.
package testutil

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinh0809it/ccsync/internal/crypto"
)

// Remote creates an empty bare git repo and returns its path, usable as a
// store repo URL.
func Remote(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	Git(t, "", "init", "--quiet", "--bare", remote)
	return remote
}

// RejectFirstPush makes the remote refuse the next push only, as a
// transient failure (network, auth) would.
func RejectFirstPush(t *testing.T, remote string) {
	t.Helper()
	marker := filepath.ToSlash(filepath.Join(remote, "rejected-once"))
	hook := "#!/bin/sh\n[ -e '" + marker + "' ] && exit 0\ntouch '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Git runs git (in dir, if set) and returns its trimmed output.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// RemoteFiles lists the files in the remote's HEAD tree.
func RemoteFiles(t *testing.T, remote string) string {
	t.Helper()
	return Git(t, remote, "ls-tree", "-r", "--name-only", "HEAD")
}

// Machine is one simulated computer: its own ccsync config, data
// directory (store clones) and Claude Code directory.
type Machine struct {
	Config, Data, Claude string
}

// NewMachine creates a machine in fresh temp dirs and switches to it.
// Passphrase hashing is made cheap: the tests are about sync, not about
// resisting brute force.
func NewMachine(t *testing.T) Machine {
	t.Helper()
	crypto.ScryptWorkFactor = 10
	base := t.TempDir()
	m := Machine{
		Config: filepath.Join(base, "config"),
		Data:   filepath.Join(base, "data"),
		Claude: filepath.Join(base, "claude"),
	}
	m.Use(t)
	return m
}

// Use points the environment at this machine.
func (m Machine) Use(t *testing.T) {
	t.Setenv("CCSYNC_CONFIG_DIR", m.Config)
	t.Setenv("CCSYNC_DATA_DIR", m.Data)
	t.Setenv("CLAUDE_CONFIG_DIR", m.Claude)
}

// JSONString is s as it appears inside a JSON string (no quotes).
func JSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// CwdLine is a transcript line recording a working directory.
func CwdLine(cwd string) string {
	return `{"type":"system","cwd":"` + JSONString(cwd) + `","version":"2.1.220"}`
}

// TitleLine is the transcript line /rename appends.
func TitleLine(title string) string {
	return `{"type":"custom-title","customTitle":"` + JSONString(title) + `"}`
}

// WriteFile writes data at path, creating directories, and backdates it so
// the session doesn't count as active.
func WriteFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// WriteSession writes a transcript for session id in Claude Code project
// folder slug and returns its path. Each line gets a trailing newline.
func WriteSession(t *testing.T, claudeDir, slug, id string, lines ...string) string {
	t.Helper()
	file := filepath.Join(claudeDir, "projects", slug, id+".jsonl")
	WriteFile(t, file, strings.Join(lines, "\n")+"\n")
	return file
}
