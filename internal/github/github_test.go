package github

import "testing"

func TestRepo(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/me/sessions.git": "me/sessions",
		"https://github.com/me/sessions":     "me/sessions",
		"git@github.com:me/my.store.git":     "me/my.store",
		"ssh://git@github.com/org/x":         "org/x",
		"https://gitlab.com/me/sessions.git": "",
		"/srv/git/sessions.git":              "",
		"https://github.com/me":              "",
	} {
		if got := Repo(url); got != want {
			t.Errorf("Repo(%q) = %q, want %q", url, got, want)
		}
	}
}
