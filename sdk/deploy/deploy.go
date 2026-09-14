// Package deploy uploads the two output directories (the well-known anchor
// and the repo base) to a static host (spec/clients.md §2, design/tooling.md §4).
package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Deployer uploads the anchor dir and the repo dir.
type Deployer interface {
	Deploy(ctx context.Context, anchorDir, repoDir string) (Result, error)
}

// Result reports what was uploaded.
type Result struct {
	Backend string `json:"backend"`
	Files   int    `json:"files"`
	Target  string `json:"target"`
}

// LocalDeployer copies both directories to a local target (rsync-compatible).
type LocalDeployer struct{ Target string }

// Deploy copies anchor → target/.well-known/keryx and repo → target/<repo base>.
func (d LocalDeployer) Deploy(_ context.Context, anchorDir, repoDir string) (Result, error) {
	if d.Target == "" {
		return Result{}, fmt.Errorf("deploy local: --target is required")
	}
	anchorTarget := filepath.Join(d.Target, ".well-known", "keryx")
	n, err := copyTree(anchorDir, anchorTarget)
	if err != nil {
		return Result{}, err
	}
	repoTarget := filepath.Join(d.Target, "keryx")
	m, err := copyTree(repoDir, repoTarget)
	if err != nil {
		return Result{}, err
	}
	return Result{Backend: "local", Files: n + m, Target: d.Target}, nil
}

func copyTree(src, dst string) (int, error) {
	count := 0
	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

// S3Deployer uploads to any S3-compatible store (AWS S3, R2, B2, MinIO).
type S3Deployer struct {
	Endpoint  string
	Bucket    string
	Prefix    string
	AccessKey string
	SecretKey string
	Secure    bool
}

// Deploy uploads both directories. Anchor goes under <prefix>/.well-known/keryx/
// and the repo under <prefix>/keryx/.
func (d S3Deployer) Deploy(ctx context.Context, anchorDir, repoDir string) (Result, error) {
	if d.Bucket == "" {
		return Result{}, fmt.Errorf("deploy s3: --bucket is required")
	}
	client, err := minio.New(d.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(d.AccessKey, d.SecretKey, ""),
		Secure: d.Secure,
	})
	if err != nil {
		return Result{}, err
	}
	prefix := strings.Trim(d.Prefix, "/")
	join := func(base string, parts ...string) string {
		all := append([]string{prefix, base}, parts...)
		return strings.Trim(strings.Join(all, "/"), "/")
	}
	count := 0
	upload := func(base, dir string) error {
		return filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			info, err := entry.Info()
			if err != nil {
				return err
			}
			key := join(base, filepath.ToSlash(rel))
			if _, err := client.PutObject(ctx, d.Bucket, key, f, info.Size(), minio.PutObjectOptions{}); err != nil {
				return err
			}
			count++
			return nil
		})
	}
	if err := upload(".well-known/keryx", anchorDir); err != nil {
		return Result{}, err
	}
	if err := upload("keryx", repoDir); err != nil {
		return Result{}, err
	}
	return Result{Backend: "s3", Files: count, Target: d.Bucket + "/" + prefix}, nil
}

// NewS3 returns an S3 deployer; secure defaults to TLS.
func NewS3(endpoint, bucket, prefix, accessKey, secretKey string, secure bool) S3Deployer {
	return S3Deployer{Endpoint: endpoint, Bucket: bucket, Prefix: prefix, AccessKey: accessKey, SecretKey: secretKey, Secure: secure}
}
