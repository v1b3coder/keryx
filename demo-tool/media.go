package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// mediaSHA256 returns the lowercase-hex SHA-256 of assets/img/<name> — the
// exact bytes copied verbatim to <site>/media/img/<name>. A linked image
// REQUIRES image_sha256 (spec/feeds.md §1.1: the preview is never a mutable
// resource; the app verifies the fetched bytes before rendering).
func mediaSHA256(name string) string {
	data, err := os.ReadFile(filepath.Join("assets", "img", name))
	if err != nil {
		panic(fmt.Sprintf("media file assets/img/%s: %v", name, err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// logoSHA256 returns the lowercase-hex SHA-256 of assets/logo.png — the
// exact bytes copied verbatim to <site>/media/logo.png. A linked logo
// REQUIRES logo_sha256 (spec/repository.md §2: the logo is never a mutable
// resource; on mismatch the app falls back to a neutral placeholder).
func logoSHA256() string {
	data, err := os.ReadFile(filepath.Join("assets", "logo.png"))
	if err != nil {
		panic(fmt.Sprintf("logo file assets/logo.png: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
