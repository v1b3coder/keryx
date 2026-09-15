package config_test

import (
	"path/filepath"
	"testing"

	"github.com/v1b3coder/keryx/sdk/config"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := config.Default(dir)
	c.RepoBase = "https://cdn.example.com/keryx"
	c.Origin = "https://company.example"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin != "https://company.example" {
		t.Fatalf("origin = %q", got.Origin)
	}
	if got.RepoBase != "https://cdn.example.com/keryx" {
		t.Fatalf("repo_base = %q", got.RepoBase)
	}
	// a missing config falls back to the default workspace
	other, err := config.Load(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if other.Origin != "" || other.Role != "operator" {
		t.Fatalf("default = %+v", other)
	}
}
