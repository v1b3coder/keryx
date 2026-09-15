// Package repo is the shared state of the publisher (design/tooling.md §4):
// the repo is the only state, and the web app can back this interface with
// object storage or a database later. DirRepo is the filesystem-backed impl.
package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Repo reads and writes the published repository (metadata + item files).
type Repo interface {
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	List(ctx context.Context, prefix string) ([]string, error)
	// Remove deletes a file (used to clean up orphaned metadata).
	Remove(ctx context.Context, path string) error
}

// DirRepo is a Repo backed by a directory.
type DirRepo struct{ Root string }

// NewDirRepo returns a DirRepo rooted at root.
func NewDirRepo(root string) *DirRepo { return &DirRepo{Root: root} }

func (r *DirRepo) abs(path string) string {
	return filepath.Join(r.Root, filepath.FromSlash(path))
}

func (r *DirRepo) Read(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(r.abs(path))
}

func (r *DirRepo) Write(_ context.Context, path string, data []byte) error {
	p := r.abs(path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (r *DirRepo) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	root := r.abs(prefix)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(r.Root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

// Remove deletes a file (used by unpublish/regeneration).
func (r *DirRepo) Remove(_ context.Context, path string) error {
	err := os.Remove(r.abs(path))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Exists reports whether path exists.
func (r *DirRepo) Exists(_ context.Context, path string) bool {
	_, err := os.Stat(r.abs(path))
	return err == nil
}

// ValidatePath rejects paths escaping the repo root.
func ValidatePath(path string) error {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return fmt.Errorf("invalid repo path %q", path)
	}
	return nil
}
