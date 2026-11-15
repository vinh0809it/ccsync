package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the local (plaintext, credential-free) ccsync configuration.
type Config struct {
	RepoURL string `json:"repo_url"`
}

// configPath is $CCSYNC_CONFIG_DIR/config.json, or the OS config dir's
// ccsync/config.json.
func configPath() (string, error) {
	if d := os.Getenv("CCSYNC_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "config.json"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ccsync", "config.json"), nil
}

// LoadConfig reads the configuration written by Init.
func LoadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("ccsync is not set up yet — run: ccsync init <git-repo-url>")
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", p, err)
	}
	return &c, nil
}

// SaveConfig writes the configuration.
func SaveConfig(c *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}
