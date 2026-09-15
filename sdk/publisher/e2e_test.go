package publisher_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/publisher"
	"github.com/v1b3coder/keryx/sdk/repo"
)

// fixedKeys pre-generates the whole key set so two runs use identical keys.
func fixedKeys(t *testing.T, dir string) *keys.DirStore {
	t.Helper()
	store := keys.NewDirStore(dir, "")
	for _, spec := range []struct {
		role keys.Role
		name string
	}{
		{keys.RoleMaster, "master"},
		{keys.RoleOps, "ops"},
		{keys.RoleAuthor, "alice"},
		{keys.RoleAuthor, "bob"},
		{keys.RoleChannel, "news"},
		{keys.RoleChannel, "security"},
		{keys.RoleEngine, "tracking"},
	} {
		k, err := keys.Generate(spec.role, spec.name)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Add(context.Background(), k); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func buildDeterministic(t *testing.T, dir string, ks *keys.DirStore) {
	t.Helper()
	pub := publisher.New(repo.NewDirRepo(filepath.Join(dir, "repo")), repo.NewDirRepo(filepath.Join(dir, "anchor")), ks)
	pub.Now = func() time.Time { return time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	if _, err := pub.Init(ctx, publisher.InitParams{
		RepoBase: "https://cdn.example.com/keryx", CompanyName: "ACME",
	}); err != nil {
		t.Fatal(err)
	}
	alice, _ := ks.Find(ctx, keys.RoleAuthor, "alice")
	bob, _ := ks.Find(ctx, keys.RoleAuthor, "bob")
	if _, err := pub.ChannelAdd(ctx, publisher.ChannelSpec{
		Name: "security", DisplayName: "Security", Threshold: 2,
		Authors: []string{alice.KeyID(), bob.KeyID()},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.ChannelAdd(ctx, publisher.ChannelSpec{Name: "news", Simple: true}); err != nil {
		t.Fatal(err)
	}
	item := draft("hello")
	if err := feed.SignItem(item, alice); err != nil {
		t.Fatal(err)
	}
	if err := feed.SignItem(item, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.Publish(ctx, publisher.PublishParams{Channel: "security", Item: item}); err != nil {
		t.Fatal(err)
	}
}

func TestDeterministicOutput(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	// the SAME keys in both workspaces: determinism means identical output
	// for identical keys, inputs and clock
	source := fixedKeys(t, filepath.Join(dirA, "keys"))
	storeB := keys.NewDirStore(filepath.Join(dirB, "keys"), "")
	for _, role := range []keys.Role{keys.RoleMaster, keys.RoleOps, keys.RoleAuthor, keys.RoleChannel, keys.RoleEngine} {
		ks, err := source.FindAll(context.Background(), role)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range ks {
			if err := storeB.Add(context.Background(), k); err != nil {
				t.Fatal(err)
			}
		}
	}
	buildDeterministic(t, dirA, source)
	buildDeterministic(t, dirB, storeB)
	// the signed artifacts are byte-identical (same keys, inputs and clock)
	for _, rel := range []string{
		"anchor/root.json", "anchor/1.root.json",
		"repo/targets.json", "repo/snapshot.json", "repo/timestamp.json",
		"repo/channels.security.json", "repo/channels.news.json",
		"repo/channels.security.authors.json",
		"repo/channels/security/hello.json",
	} {
		a, err := os.ReadFile(filepath.Join(dirA, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		b, err := os.ReadFile(filepath.Join(dirB, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s differs between identical runs", rel)
		}
	}
}

// TestAuthorToCIToOperator simulates the three role contexts of
// design/tooling.md §3: an author signs on their own machine (author key
// only, no repo), CI publishes (channel + ops keys, no master), and the
// operator stages a ceremony (master key) that CI then applies.
func TestAuthorToCIToOperator(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	base := repo.NewDirRepo(filepath.Join(dir, "repo"))
	anchor := repo.NewDirRepo(filepath.Join(dir, "anchor"))

	// operator machine: master + ops
	opKeys := keys.NewDirStore(filepath.Join(dir, "keys-operator"), "")
	ops, _ := keys.Generate(keys.RoleOps, "ops")
	master, _ := keys.Generate(keys.RoleMaster, "master")
	for _, k := range []*keys.Key{master, ops} {
		if err := opKeys.Add(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	operator := publisher.New(base, anchor, opKeys)
	operator.Now = func() time.Time { return time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC) }
	if _, err := operator.Init(ctx, publisher.InitParams{
		RepoBase: "https://cdn.example.com/keryx", CompanyName: "ACME",
	}); err != nil {
		t.Fatal(err)
	}

	// CI machine: channel + ops keys (never the master key)
	ciKeys := keys.NewDirStore(filepath.Join(dir, "keys-ci"), "")
	ci, _ := keys.Generate(keys.RoleChannel, "news")
	if err := ciKeys.Add(ctx, ci); err != nil {
		t.Fatal(err)
	}
	if err := ciKeys.Add(ctx, ops); err != nil {
		t.Fatal(err)
	}
	ciPub := publisher.New(base, anchor, ciKeys)
	ciPub.Now = operator.Now

	// operator stages the channel creation; CI applies it (the CI machine
	// never holds the master key)
	newsBundle := filepath.Join(dir, "bundle-news")
	if _, err := operator.StageChannelAdd(ctx, publisher.ChannelSpec{
		Name: "news", Simple: true,
	}, newsBundle, ""); err != nil {
		t.Fatalf("stage news: %v", err)
	}
	if _, err := ciPub.Apply(ctx, newsBundle, ""); err != nil {
		t.Fatalf("apply news: %v", err)
	}

	// author machine: author key only, no repo, no anchor
	authorKeys := keys.NewDirStore(filepath.Join(dir, "keys-author"), "")
	alice, _ := keys.Generate(keys.RoleAuthor, "alice")
	if err := authorKeys.Add(ctx, alice); err != nil {
		t.Fatal(err)
	}
	item := draft("signed-on-author-machine")
	if err := feed.SignItem(item, alice); err != nil {
		t.Fatal(err)
	}
	// the author never sees the repo: sign is pure item signing
	if _, err := os.Stat(filepath.Join(dir, "keys-author", "repo")); !os.IsNotExist(err) {
		t.Fatal("author machine unexpectedly has a repo")
	}

	// CI publishes the author-signed item with the channel key
	if _, err := ciPub.Publish(ctx, publisher.PublishParams{Channel: "news", Item: item}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := ciPub.Validate(ctx); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// operator stages a ceremony, CI applies it
	bundleDir := filepath.Join(dir, "bundle")
	if _, err := operator.StageChannelAdd(ctx, publisher.ChannelSpec{
		Name: "security", DisplayName: "Security",
	}, bundleDir, ""); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := ciPub.Apply(ctx, bundleDir, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	res, err := ciPub.Validate(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(res.Channels) != 2 {
		t.Fatalf("channels = %v", res.Channels)
	}
	// the master key never left the operator machine
	if _, err := ciKeys.Find(ctx, keys.RoleMaster, ""); err == nil {
		t.Fatal("master key is on the CI machine")
	}
}

// TestCLIEndToEnd runs the actual `pub` binary through the reference
// workflow: init → channel add → item sign → publish → validate → deploy.
func TestCLIEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI e2e in -short mode")
	}
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	run := func(args ...string) string {
		t.Helper()
		full := append([]string{"run", "./cmd/pub", "--workspace", ws}, args...)
		cmd := exec.Command("go", full...)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pub %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "--domain", "company.example", "--name", "ACME s.r.o.", "--base", "https://cdn.example.com/keryx")
	run("channel", "add", "news", "--simple")
	run("keys", "generate", "author", "--role", "author")
	// author side: sign a draft without any repo access
	draftPath := filepath.Join(dir, "draft.json")
	if err := os.WriteFile(draftPath, []byte(`{"id":"hello","title":"Hello","content_html":"<p>Hi</p>","date_published":"2026-03-14T10:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	signedPath := filepath.Join(dir, "signed.json")
	run("item", "sign", "--file", draftPath, "--out", signedPath)
	run("publish", "--channel", "news", "--file", signedPath)
	if out := run("validate"); !bytes.Contains([]byte(out), []byte("OK")) {
		t.Fatalf("validate: %s", out)
	}
	target := filepath.Join(dir, "site")
	run("deploy", "local", "--target", target)
	for _, rel := range []string{".well-known/keryx/root.json", "keryx/targets.json", "keryx/channels.news.json"} {
		if _, err := os.Stat(filepath.Join(target, rel)); err != nil {
			t.Fatalf("deployed %s: %v", rel, err)
		}
	}
}
