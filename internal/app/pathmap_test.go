package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPathMapApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("expectations use POSIX destinations")
	}
	m := PathMap{
		{From: "/home/alice", To: "/Users/alice"},
		{From: "/home/alice/work/", To: "/Volumes/work"},
		{From: `C:\Users\Alice`, To: "/Users/alice/win"},
	}
	for in, want := range map[string]string{
		"/home/alice":               "/Users/alice",
		"/home/alice/dev/app":       "/Users/alice/dev/app",
		"/home/alice/work/api":      "/Volumes/work/api", // longest From wins
		"/home/alicex/app":          "/home/alicex/app",  // whole components only
		"/srv/other":                "/srv/other",        // no rule: kept
		`C:\Users\Alice\dev\my app`: "/Users/alice/win/dev/my app",
	} {
		if got := m.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRule(t *testing.T) {
	r, err := ParseRule(" /home/a = /Users/a ")
	if err != nil || r != (PathRule{"/home/a", "/Users/a"}) {
		t.Fatalf("ParseRule = %+v %v", r, err)
	}
	for _, bad := range []string{"", "/a", "=/b", "/a="} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) accepted", bad)
		}
	}
}

func TestSuggestPathMap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX homes")
	}
	home := filepath.Join(t.TempDir(), "me")
	os.MkdirAll(home, 0o700)
	t.Setenv("HOME", home)
	got := SuggestPathMap([]string{
		"/home/zz-nobody-here/dev/a",
		"/home/zz-nobody-here/dev/b",
		home + "/already/here",
		"/srv/shared/x",
	})
	if len(got) != 1 || got[0] != (PathRule{"/home/zz-nobody-here", home}) {
		t.Fatalf("SuggestPathMap = %+v", got)
	}
}
