// Package config holds the role-scoped workspace layout
// (design/tooling.md §3.2). The repo is the only shared state; the config
// only points at it.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is one workspace's layout and role.
type Config struct {
	Workspace string `json:"workspace"`
	Role      string `json:"role"` // operator | ci | author
	RepoBase  string `json:"repo_base,omitempty"`
	// Origin is the join origin (the company's own HTTPS origin whose
	// /.well-known/keryx/root.json anchors the protocol). It is not part of
	// the signed metadata, so it is persisted here.
	Origin string `json:"origin,omitempty"`
}

// Default returns the default config for a workspace directory.
func Default(workspace string) Config {
	return Config{Workspace: workspace, Role: "operator"}
}

// Path returns the config file path.
func (c Config) Path() string { return filepath.Join(c.Workspace, "keryx.json") }

// RepoDir is the repo base directory.
func (c Config) RepoDir() string { return filepath.Join(c.Workspace, "repo") }

// AnchorDir is the well-known root anchor directory.
func (c Config) AnchorDir() string { return filepath.Join(c.Workspace, "anchor") }

// KeysDir is the role-scoped key store directory.
func (c Config) KeysDir() string { return filepath.Join(c.Workspace, "keys") }

// Load reads a config, falling back to the default when absent.
func Load(workspace string) (Config, error) {
	path := filepath.Join(workspace, "keryx.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(workspace), nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if c.Workspace == "" {
		c.Workspace = workspace
	}
	if c.Role == "" {
		c.Role = "operator"
	}
	return c, nil
}

// Save writes the config.
func (c Config) Save() error {
	if err := os.MkdirAll(c.Workspace, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.Path(), data, 0o644)
}
