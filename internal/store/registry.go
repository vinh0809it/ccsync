package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// A machine can be set up against several stores (say "work" and
// "personal"), each its own repo with its own passphrase. The registry is
// a plaintext, credential-free file listing them:
//
//	$CCSYNC_CONFIG_DIR/config.json   (default: <OS config dir>/ccsync/config.json)
//
// A git store's clone, and every store's lock file, live under the data
// directory:
//
//	$CCSYNC_DATA_DIR/stores/<name>        (default: ~/.local/share/ccsync/...)
//	$CCSYNC_DATA_DIR/locks/<name>.lock

// Entry is one store this machine is set up for.
type Entry struct {
	Name    string `json:"name"`
	Kind    Kind   `json:"kind,omitempty"`     // empty means git (0.2 registries)
	RepoURL string `json:"repo_url,omitempty"` // git stores
	Dir     string `json:"dir"`                // git: local clone; folder: the synced folder
}

// Location is where the store is shared: the repo URL, or the folder.
func (e Entry) Location() string {
	if e.Kind == KindFolder {
		return e.Dir
	}
	return e.RepoURL
}

// Registry lists this machine's stores.
type Registry struct {
	Current string  `json:"current,omitempty"` // used when no store is named
	Stores  []Entry `json:"stores"`
}

// ErrNoStore means the machine is not set up for any store yet.
var ErrNoStore = errors.New("ccsync is not set up yet — run: ccsync init <git-repo-url>")

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// CheckName validates a store name: lowercase letters, digits, - and _.
func CheckName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid store name %q: use up to 32 lowercase letters, digits, - or _", name)
	}
	return nil
}

func registryPath() (string, error) {
	if d := os.Getenv("CCSYNC_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "config.json"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ccsync", "config.json"), nil
}

func dataDir() (string, error) {
	if d := os.Getenv("CCSYNC_DATA_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "ccsync"), nil
}

// DefaultDir is where a new git store's clone goes.
func DefaultDir(name string) (string, error) {
	d, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "stores", name), nil
}

// LoadRegistry reads the registry; a machine with none has an empty one.
// The single-store file of ccsync 0.1 ({"repo_url": ...}) is read as one
// store named "default" at its old clone location.
func LoadRegistry() (*Registry, error) {
	p, err := registryPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &Registry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var r struct {
		Registry
		LegacyRepoURL string `json:"repo_url"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", p, err)
	}
	if r.LegacyRepoURL != "" && len(r.Stores) == 0 {
		d, err := dataDir()
		if err != nil {
			return nil, err
		}
		return &Registry{Current: "default", Stores: []Entry{{
			Name: "default", Kind: KindGit, RepoURL: r.LegacyRepoURL, Dir: filepath.Join(d, "store"),
		}}}, nil
	}
	return &r.Registry, nil
}

// Save writes the registry.
func (r *Registry) Save() error {
	p, err := registryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(p, append(data, '\n')); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// Get returns the named store, or the current one when name is empty.
func (r *Registry) Get(name string) (Entry, error) {
	if len(r.Stores) == 0 {
		return Entry{}, ErrNoStore
	}
	if name == "" {
		name = r.Current
	}
	if name == "" && len(r.Stores) == 1 {
		return r.Stores[0], nil
	}
	if i := r.index(name); i >= 0 {
		return r.Stores[i], nil
	}
	if name == "" {
		return Entry{}, fmt.Errorf("several stores are set up and none is selected — run: ccsync store use <name>")
	}
	return Entry{}, fmt.Errorf("no store named %q on this machine (see: ccsync store list)", name)
}

func (r *Registry) index(name string) int {
	return slices.IndexFunc(r.Stores, func(e Entry) bool { return e.Name == name })
}

// Put adds or replaces a store and makes it current.
func (r *Registry) Put(e Entry) {
	if i := r.index(e.Name); i >= 0 {
		r.Stores[i] = e
	} else {
		r.Stores = append(r.Stores, e)
	}
	r.Current = e.Name
}

// Use makes the named store current.
func (r *Registry) Use(name string) error {
	if r.index(name) < 0 {
		return fmt.Errorf("no store named %q on this machine (see: ccsync store list)", name)
	}
	r.Current = name
	return nil
}

// Remove drops a store from the registry and returns it.
func (r *Registry) Remove(name string) (Entry, error) {
	i := r.index(name)
	if i < 0 {
		return Entry{}, fmt.Errorf("no store named %q on this machine", name)
	}
	e := r.Stores[i]
	r.Stores = slices.Delete(r.Stores, i, i+1)
	if r.Current == name {
		r.Current = ""
		if len(r.Stores) == 1 {
			r.Current = r.Stores[0].Name
		}
	}
	return e, nil
}
