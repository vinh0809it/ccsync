package gui

import (
	"path/filepath"
	"testing"

	"github.com/vinh0809it/ccsync/internal/store"
)

func TestStoreLink(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/me/sessions.git":            "https://github.com/me/sessions",
		"https://me:secret@github.com/me/sessions.git/": "https://github.com/me/sessions",
		"http://git.lan:3000/me/s":                      "https://git.lan:3000/me/s",
		"git@github.com:me/sessions.git":                "https://github.com/me/sessions",
		"ssh://git@gitlab.com:2222/grp/sub/s.git":       "https://gitlab.com/grp/sub/s",
		"file:///srv/git/s.git":                         "/srv/git/s.git",
		"ftp://x/y":                                     "",
		"":                                              "",
	} {
		if got := storeLink(store.Entry{Kind: store.KindGit, RepoURL: remote}); got != want {
			t.Errorf("storeLink(%q) = %q, want %q", remote, got, want)
		}
	}
	local := filepath.Join(t.TempDir(), "s.git") // absolute on every OS
	if got := storeLink(store.Entry{Kind: store.KindGit, RepoURL: local}); got != local {
		t.Errorf("local repo link = %q, want %q", got, local)
	}
	if got := storeLink(store.Entry{Kind: store.KindFolder, Dir: "/home/me/Dropbox/s"}); got != "/home/me/Dropbox/s" {
		t.Errorf("folder store link = %q", got)
	}
}
