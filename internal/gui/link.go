package gui

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/vinh0809it/ccsync/internal/store"
)

// storeLink is what "open the store" opens: a git remote's web page
// (https://host/owner/repo, without credentials), or the folder of a
// synced-folder store or a local repo. "" when there's nothing to open.
func storeLink(e store.Entry) string {
	if e.Kind == store.KindFolder {
		return e.Dir
	}
	remote := strings.TrimSpace(e.RepoURL)
	if filepath.IsAbs(remote) {
		return remote
	}
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		switch u.Scheme {
		case "http", "https":
			host = u.Host // a custom web port is kept
		case "ssh", "git", "git+ssh":
			host = u.Hostname() // the web page isn't on the ssh port
		default:
			return ""
		}
		path = u.Path
	} else if at, colon := strings.Index(remote, "@"), strings.Index(remote, ":"); colon > at && !strings.Contains(remote, "://") {
		// scp-like: git@github.com:owner/repo.git
		host, path = remote[at+1:colon], remote[colon+1:]
	} else if u, err := url.Parse(remote); err == nil && u.Scheme == "file" {
		return u.Path
	} else {
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return ""
	}
	return "https://" + host + "/" + path
}
