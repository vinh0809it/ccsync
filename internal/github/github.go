// Package github talks to GitHub through the user's GitHub CLI (`gh`), so
// ccsync never handles a token: creating a private repo for a store, and
// checking that a store repo isn't public.
package github

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// ErrNoCLI means `gh` is not installed or not logged in.
var ErrNoCLI = errors.New("the GitHub CLI (gh) is not installed or not logged in — see https://cli.github.com, then run: gh auth login")

func gh(args ...string) (string, error) { return ghAs("", args...) }

// ghAs runs gh as account (the active gh account when empty).
func ghAs(account string, args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	if account != "" {
		token, err := Token(account)
		if err != nil {
			return "", err
		}
		cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", ErrNoCLI
		}
		return "", fmt.Errorf("gh %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Available reports whether `gh` is installed and logged in.
func Available() bool {
	_, err := gh("auth", "status")
	return err == nil
}

// repoPattern matches GitHub repo URLs in their https, ssh and scp forms.
var repoPattern = regexp.MustCompile(`^(?:https://github\.com/|ssh://git@github\.com/|git@github\.com:)([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// Repo returns "owner/name" for a GitHub repo URL, or "" for any other URL.
func Repo(url string) string {
	m := repoPattern.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// IsPublic reports whether the GitHub repo at url is public. It returns an
// error when that can't be determined (not a GitHub URL, no `gh`, no
// access).
func IsPublic(url, account string) (bool, error) {
	repo := Repo(url)
	if repo == "" {
		return false, fmt.Errorf("%s is not a GitHub repo URL", url)
	}
	vis, err := ghAs(account, "repo", "view", repo, "--json", "visibility", "--jq", ".visibility")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(vis, "public"), nil
}

// CreatePrivate creates a private repo ("name" under the logged-in
// account, or "owner/name") and returns its https clone URL. Git reaches it
// with gh's credentials when `gh auth setup-git` has been run (gh auth
// login offers it).
func CreatePrivate(name, account string) (string, error) {
	if !regexp.MustCompile(`^(?:[\w.-]+/)?[\w.-]+$`).MatchString(name) {
		return "", fmt.Errorf("invalid repo name %q", name)
	}
	if _, err := ghAs(account, "repo", "create", name, "--private",
		"--description", "Encrypted Claude Code session store (ccsync)"); err != nil {
		return "", err
	}
	url, err := ghAs(account, "repo", "view", name, "--json", "url", "--jq", ".url")
	if err != nil {
		return "", err
	}
	return url + ".git", nil
}

// accountLine matches gh auth status's "Logged in to github.com account NAME".
var accountLine = regexp.MustCompile(`Logged in to github\.com account ([\w-]+)`)

// Accounts lists the GitHub accounts gh is logged in to, the active one
// first; none when gh is missing.
func Accounts() []string {
	out, err := gh("auth", "status", "--hostname", "github.com")
	if err != nil {
		return nil
	}
	return parseAccounts(out)
}

func parseAccounts(status string) []string {
	var names []string
	for _, m := range accountLine.FindAllStringSubmatch(status, -1) {
		names = append(names, m[1])
	}
	return names
}

var accountName = regexp.MustCompile(`^[\w-]+$`)

// Token returns account's GitHub token from gh, for git to authenticate
// as that account.
func Token(account string) (string, error) {
	if !accountName.MatchString(account) {
		return "", fmt.Errorf("invalid GitHub account %q", account)
	}
	out, err := exec.Command("gh", "auth", "token", "--hostname", "github.com", "--user", account).Output()
	if err != nil {
		return "", fmt.Errorf("gh has no login for the GitHub account %s — run: gh auth login", account)
	}
	return strings.TrimSpace(string(out)), nil
}
