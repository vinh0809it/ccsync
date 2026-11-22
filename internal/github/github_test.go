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

func TestParseAccounts(t *testing.T) {
	status := `github.com
  ✓ Logged in to github.com account alice (keyring)
  - Active account: true
  ✓ Logged in to github.com account bob-work (keyring)
  - Active account: false`
	if got := parseAccounts(status); len(got) != 2 || got[0] != "alice" || got[1] != "bob-work" {
		t.Fatalf("accounts = %v", got)
	}
	if _, err := Token("bad name; rm"); err == nil {
		t.Fatal("Token accepted an invalid account name")
	}
}
