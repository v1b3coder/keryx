package publisher_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"encoding/json"

	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/join"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/publisher"
	"github.com/v1b3coder/keryx/sdk/repo"
	"github.com/v1b3coder/keryx/sdk/tufrepo"
)

type env struct {
	t   *testing.T
	pub *publisher.Publisher
	ks  *keys.DirStore
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	ks := keys.NewDirStore(filepath.Join(dir, "keys"), "")
	pub := publisher.New(repo.NewDirRepo(filepath.Join(dir, "repo")), repo.NewDirRepo(filepath.Join(dir, "anchor")), ks)
	pub.Now = func() time.Time { return time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC) }
	pub.GenerateKeys = true // single-step: the operator machine holds every key
	ctx := context.Background()
	if _, err := pub.Init(ctx, publisher.InitParams{
		RepoBase: "https://cdn.example.com/keryx", CompanyName: "ACME s.r.o.",
	}); err != nil {
		t.Fatalf("init: %v", err)
	}
	return &env{t: t, pub: pub, ks: ks}
}

func (e *env) ctx() context.Context { return context.Background() }

func draft(id string) map[string]any {
	return map[string]any{
		"id":             id,
		"title":          "Title " + id,
		"content_html":   "<p>Body</p>",
		"date_published": "2026-03-14T10:00:00Z",
	}
}

func (e *env) signDraft(channel string, item map[string]any) map[string]any {
	e.t.Helper()
	authors, err := e.pub.AuthorList(e.ctx(), channel)
	if err != nil {
		e.t.Fatalf("author list: %v", err)
	}
	key, err := e.ks.Get(e.ctx(), authors[0])
	if err != nil {
		e.t.Fatalf("author key: %v", err)
	}
	if err := feed.SignItem(item, key); err != nil {
		e.t.Fatalf("sign item: %v", err)
	}
	return item
}

func TestInitValidate(t *testing.T) {
	e := newEnv(t)
	res, err := e.pub.Validate(e.ctx())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if res.Company != "ACME s.r.o." {
		t.Fatalf("company = %q", res.Company)
	}
	// root anchor exists, repo base has no root
	if _, err := os.Stat(filepath.Join(e.pub.Anchor.(*repo.DirRepo).Root, "root.json")); err != nil {
		t.Fatalf("anchor root.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.pub.Base.(*repo.DirRepo).Root, "root.json")); !os.IsNotExist(err) {
		t.Fatalf("root.json must not be in the repo base")
	}
}

func TestDefaultRootExpiry(t *testing.T) {
	e := newEnv(t)
	st, err := tufrepo.Load(e.ctx(), e.pub.Base, e.pub.Anchor)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	root := tufrepo.DefaultExpiries().Root
	if root < 5*365*24*time.Hour {
		t.Fatalf("root default %s is too short (want a years-long backstop)", root)
	}
	want := e.pub.Now().Add(root)
	if !st.Root.Signed.Expires.Equal(want) {
		t.Fatalf("root expires %s, want %s", st.Root.Signed.Expires, want)
	}
}

func TestChannelAddAuthoredByDefault(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{
		Name: "news", DisplayName: "News", Description: "News channel",
	}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	channels, err := e.pub.Channels(e.ctx())
	if err != nil {
		t.Fatalf("channels: %v", err)
	}
	if len(channels) != 1 || channels[0].Name != "news" || channels[0].Mode != "authored" {
		t.Fatalf("channels = %+v", channels)
	}
	if channels[0].DisplayName != "News" {
		t.Fatalf("display name = %q", channels[0].DisplayName)
	}
}

func TestPublishAuthoredAndValidate(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "security"}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	item := e.signDraft("security", draft("advisory-1"))
	if _, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "security", Item: item}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	ids, err := e.pub.ItemIDs(e.ctx(), "security")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(ids) != 1 || ids[0] != "advisory-1" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestPublishRefusesUnsignedAuthoredItem(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "security"}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	_, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "security", Item: draft("unsigned")})
	if err == nil {
		t.Fatal("publish accepted an unsigned authored item")
	}
}

func TestPublishRefusesBadAuthorSignature(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "security"}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	item := e.signDraft("security", draft("tampered"))
	item["title"] = "tampered after signing"
	if _, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "security", Item: item}); err == nil {
		t.Fatal("publish accepted a tampered item")
	}
}

func TestSimpleModeSignsWithChannelKey(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "offers", Simple: true}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	if _, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "offers", Item: draft("sale")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestUnpublishDropsItem(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "offers", Simple: true}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	if _, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "offers", Item: draft("sale")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := e.pub.Unpublish(e.ctx(), "offers", "sale"); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	ids, err := e.pub.ItemIDs(e.ctx(), "offers")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v after unpublish", ids)
	}
}

func TestChannelModeSwitchResignsItems(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "news", Simple: true}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	if _, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "news", Item: draft("one")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := e.pub.ChannelMode(e.ctx(), "news", "authored"); err != nil {
		t.Fatalf("channel mode: %v", err)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate after switch to authored: %v", err)
	}
	if _, err := e.pub.ChannelMode(e.ctx(), "news", "simple"); err != nil {
		t.Fatalf("channel mode back: %v", err)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate after switch to simple: %v", err)
	}
}

func TestAuthorRevokeRefusesLastAuthor(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "security"}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	authors, err := e.pub.AuthorList(e.ctx(), "security")
	if err != nil || len(authors) != 1 {
		t.Fatalf("authors = %v, %v", authors, err)
	}
	if _, err := e.pub.AuthorRevoke(e.ctx(), "security", authors[0]); err == nil {
		t.Fatal("revoked the last author")
	}
}

func TestKeySeparationRefused(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "security"}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	channels, _ := e.pub.Channels(e.ctx())
	channelKey := channels[0].KeyIDs[0]
	if _, err := e.pub.AuthorAdd(e.ctx(), "security", channelKey); err == nil {
		t.Fatal("accepted the channel key as an author key")
	}
}

func TestRotateChannelKeyKeepsItemsVerifying(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "offers", Simple: true}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	if _, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "offers", Item: draft("sale")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := e.pub.RotateChannelKey(e.ctx(), "offers"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate after rotation: %v", err)
	}
}

func TestPrivateFeedBuildUpdateExpire(t *testing.T) {
	e := newEnv(t)
	engine, err := e.ks.Find(e.ctx(), keys.RoleEngine, "")
	if err != nil {
		engine, err = keys.Generate(keys.RoleEngine, "tracking-engine")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.ks.Add(e.ctx(), engine); err != nil {
			t.Fatal(err)
		}
	}
	feedURL := "https://eshop.example.com/channels/tracking/" + mustToken(t) + "/feed.json"
	doc, err := e.pub.BuildPrivateDocument(e.ctx(), engine.KeyID(), feedURL, "tracking",
		[]map[string]any{draft("order-1")}, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	keyMap := map[string]ed25519.PublicKey{engine.KeyID(): engine.Public()}
	ver, err := feed.VerifyPrivateDocument(doc, keyMap, 1, feedURL, "tracking", 0)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ver.Version != 1 {
		t.Fatalf("version = %d", ver.Version)
	}
	// update: absence = removed
	doc2, err := e.pub.UpdatePrivateDocument(e.ctx(), engine.KeyID(), doc, nil, time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if doc2["version"] != int64(2) {
		t.Fatalf("update version = %v", doc2["version"])
	}
	if _, err := feed.VerifyPrivateDocument(doc2, keyMap, 1, feedURL, "tracking", 1); err != nil {
		t.Fatalf("verify update: %v", err)
	}
	doc3, err := e.pub.ExpirePrivateDocument(e.ctx(), engine.KeyID(), doc2)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if doc3["expired"] != true {
		t.Fatalf("expired = %v", doc3["expired"])
	}
	ver3, err := feed.VerifyPrivateDocument(doc3, keyMap, 1, feedURL, "tracking", 2)
	if err != nil {
		t.Fatalf("verify expire: %v", err)
	}
	if !ver3.Closed {
		t.Fatal("expired feed not reported closed")
	}
}

func TestJoinPayload(t *testing.T) {
	payload, err := join.BuildPayload([]string{"news", "security"}, []string{"https://x.example/feed.json"})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	url, err := join.JoinURL("https://company.example", payload)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	origin, parsed, err := join.ParseJoinURL(url)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if origin != "https://company.example" || parsed.V != 1 || len(parsed.Channels) != 2 {
		t.Fatalf("parsed = %q %+v", origin, parsed)
	}
	if _, err := join.QR(url, 128); err != nil {
		t.Fatalf("qr: %v", err)
	}
}

func TestRefreshTimestampBumpsVersion(t *testing.T) {
	e := newEnv(t)
	before, err := e.pub.Validate(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.pub.RefreshTimestamp(e.ctx())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.Version <= 0 {
		t.Fatalf("timestamp version = %d", res.Version)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate after refresh: %v", err)
	}
	_ = before
}

func TestRotateRootChain(t *testing.T) {
	e := newEnv(t)
	res, err := e.pub.RotateRoot(e.ctx(), false)
	if err != nil {
		t.Fatalf("rotate root: %v", err)
	}
	if res.Version != 2 {
		t.Fatalf("root version = %d", res.Version)
	}
	// the anchor keeps every version
	for _, name := range []string{"root.json", "1.root.json", "2.root.json"} {
		if _, err := os.Stat(filepath.Join(e.pub.Anchor.(*repo.DirRepo).Root, name)); err != nil {
			t.Fatalf("anchor %s: %v", name, err)
		}
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate after root rotation: %v", err)
	}
}

func TestRotateRootRefreshesExpiry(t *testing.T) {
	e := newEnv(t)
	now := e.pub.Now()
	short := 30 * 24 * time.Hour
	e.pub.Exp = tufrepo.DefaultExpiries()
	e.pub.Exp.Root = short
	if _, err := e.pub.RotateRoot(e.ctx(), false); err != nil {
		t.Fatalf("rotate root: %v", err)
	}
	st, err := tufrepo.Load(e.ctx(), e.pub.Base, e.pub.Anchor)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if st.Root.Signed.Version != 2 {
		t.Fatalf("root version = %d, want 2", st.Root.Signed.Version)
	}
	// a rotation renews the window from the configured expiry; it must not
	// inherit the previous root's remaining lifetime
	want := now.Add(short)
	if !st.Root.Signed.Expires.Equal(want) {
		t.Fatalf("rotated root expires %s, want refreshed %s", st.Root.Signed.Expires, want)
	}
}

func TestValidateRejectsTamperedItem(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.ChannelAdd(e.ctx(), publisher.ChannelSpec{Name: "news"}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	if _, err := e.pub.Publish(e.ctx(), publisher.PublishParams{Channel: "news", Item: e.signDraft("news", draft("one"))}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// tamper with the item bytes on disk
	itemPath := filepath.Join(e.pub.Base.(*repo.DirRepo).Root, "channels", "news", "one.json")
	data, err := os.ReadFile(itemPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0x01
	if err := os.WriteFile(itemPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pub.Validate(e.ctx()); err == nil {
		t.Fatal("validate accepted a tampered item")
	}
}

func mustToken(t *testing.T) string {
	t.Helper()
	tok, err := feed.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base64.RawURLEncoding.DecodeString(tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTwoStepCeremony(t *testing.T) {
	dir := t.TempDir()
	base := repo.NewDirRepo(filepath.Join(dir, "repo"))
	anchor := repo.NewDirRepo(filepath.Join(dir, "anchor"))
	operatorKeys := keys.NewDirStore(filepath.Join(dir, "keys-operator"), "")
	ciKeys := keys.NewDirStore(filepath.Join(dir, "keys-ci"), "")

	// operator machine: master + ops backup key
	ops, err := keys.Generate(keys.RoleOps, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorKeys.Add(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	operator := publisher.New(base, anchor, operatorKeys)
	operator.Now = func() time.Time { return time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC) }
	operator.GenerateKeys = true // the offline machine mints the channel key into the bundle
	if _, err := operator.Init(context.Background(), publisher.InitParams{
		RepoBase: "https://cdn.example.com/keryx", CompanyName: "ACME",
	}); err != nil {
		t.Fatalf("init: %v", err)
	}
	// CI machine: the same ops key (snapshot/timestamp), no master
	if err := ciKeys.Add(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	ci := publisher.New(base, anchor, ciKeys)
	ci.Now = operator.Now

	// stage on the offline machine: master-signed targets.json + bundle
	authorA, err := keys.Generate(keys.RoleAuthor, "author-a")
	if err != nil {
		t.Fatal(err)
	}
	authorB, err := keys.Generate(keys.RoleAuthor, "author-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []*keys.Key{authorA, authorB} {
		if err := operatorKeys.Add(context.Background(), k); err != nil {
			t.Fatal(err)
		}
	}
	bundleDir := filepath.Join(dir, "bundle")
	if _, err := operator.StageChannelAdd(context.Background(), publisher.ChannelSpec{
		Name: "security", DisplayName: "Security", Threshold: 2,
		Authors: []string{authorA.KeyID(), authorB.KeyID()},
	}, bundleDir, ""); err != nil {
		t.Fatalf("stage: %v", err)
	}
	// the live repo is untouched until apply
	live, err := tufrepo.Load(context.Background(), base, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if live.Channels["security"] != nil {
		t.Fatal("stage wrote the live repo")
	}
	// apply on the CI machine: installs the channel key, creates role metadata
	if _, err := ci.Apply(context.Background(), bundleDir, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	res, err := ci.Validate(context.Background())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(res.Channels) != 1 || res.Channels[0] != "security" {
		t.Fatalf("channels = %v", res.Channels)
	}
	if _, err := ci.AuthorList(context.Background(), "security"); err != nil {
		t.Fatalf("authors: %v", err)
	}
}

func TestRefreshTimestampExpires(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pub.RefreshTimestampExpires(e.ctx(), time.Hour); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := e.pub.ValidateStrict(e.ctx()); err != nil {
		t.Fatalf("strict validate: %v", err)
	}
}

func TestCompanySetLogoRules(t *testing.T) {
	e := newEnv(t)
	// a linked logo without logo_sha256 is refused
	if _, err := e.pub.CompanySet(e.ctx(), "", "https://cdn.example.com/logo.png", ""); err == nil {
		t.Fatal("accepted a linked logo without logo_sha256")
	}
	// linked + sha256 is accepted and recorded
	if _, err := e.pub.CompanySet(e.ctx(), "", "https://cdn.example.com/logo.png", "abc123"); err != nil {
		t.Fatalf("linked logo: %v", err)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// an inline data URL is self-authenticated (no logo_sha256)
	if _, err := e.pub.CompanySet(e.ctx(), "", "data:image/png;base64,AAAA", ""); err != nil {
		t.Fatalf("inline logo: %v", err)
	}
	if _, err := e.pub.Validate(e.ctx()); err != nil {
		t.Fatalf("validate after inline logo: %v", err)
	}
}

func TestAuthorRevokeResignsItems(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	alice, err := keys.Generate(keys.RoleAuthor, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := keys.Generate(keys.RoleAuthor, "bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []*keys.Key{alice, bob} {
		if err := e.ks.Add(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pub.ChannelAdd(ctx, publisher.ChannelSpec{
		Name: "security", Threshold: 1, Authors: []string{alice.KeyID(), bob.KeyID()},
	}); err != nil {
		t.Fatalf("channel add: %v", err)
	}
	item := draft("hello")
	if err := feed.SignItem(item, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pub.Publish(ctx, publisher.PublishParams{Channel: "security", Item: item}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// revoking the author whose key signed the item must re-sign it with the
	// remaining author, otherwise it would be dropped on the next client fetch
	if _, err := e.pub.AuthorRevoke(ctx, "security", alice.KeyID()); err != nil {
		t.Fatalf("author revoke: %v", err)
	}
	if _, err := e.pub.Validate(ctx); err != nil {
		t.Fatalf("validate after revoke: %v", err)
	}
	// the item still verifies against the remaining author
	data, err := e.pub.Base.(*repo.DirRepo).Read(ctx, "channels/security/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	obj, err := feed.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := feed.VerifyItem(obj, map[string]ed25519.PublicKey{bob.KeyID(): bob.Public()}, 1, nil, 0); err != nil {
		t.Fatalf("item does not verify under the remaining author: %v", err)
	}
}

func TestRotateRootThenMasterCeremony(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	if _, err := e.pub.RotateRoot(ctx, false); err != nil {
		t.Fatalf("rotate root: %v", err)
	}
	// after the rotation every master ceremony must use the NEW master key
	// (root.json authorizes only it)
	if _, err := e.pub.CompanySet(ctx, "ACME a.s.", "", ""); err != nil {
		t.Fatalf("company set after rotate-root: %v", err)
	}
	if _, err := e.pub.ChannelAdd(ctx, publisher.ChannelSpec{Name: "offers", Simple: true}); err != nil {
		t.Fatalf("channel add after rotate-root: %v", err)
	}
	if _, err := e.pub.RotateRoot(ctx, false); err != nil {
		t.Fatalf("second rotate root: %v", err)
	}
	if _, err := e.pub.Validate(ctx); err != nil {
		t.Fatalf("validate after two rotations: %v", err)
	}
}

func TestValidateDetectsBrokenRootChain(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	if _, err := e.pub.RotateRoot(ctx, false); err != nil {
		t.Fatalf("rotate root: %v", err)
	}
	// corrupt the released 2.root.json: the current root is no longer chained to
	// the anchor, which compliant clients treat as a chain break
	anchor := e.pub.Anchor.(*repo.DirRepo).Root
	path := filepath.Join(anchor, "2.root.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc["signed"].(map[string]any)["version"] = float64(99)
	tampered, _ := json.Marshal(doc)
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pub.Validate(ctx); err == nil {
		t.Fatal("validate accepted a broken root chain")
	}
}

func TestChannelRemoveDropsMetadata(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	if _, err := e.pub.ChannelAdd(ctx, publisher.ChannelSpec{Name: "news", Simple: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pub.Publish(ctx, publisher.PublishParams{Channel: "news", Item: draft("hello")}); err != nil {
		t.Fatal(err)
	}
	root := e.pub.Base.(*repo.DirRepo).Root
	if _, err := e.pub.ChannelRemove(ctx, "news"); err != nil {
		t.Fatalf("channel remove: %v", err)
	}
	if _, err := e.pub.Validate(ctx); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// the removed channel's metadata and items are gone from the base
	for _, rel := range []string{"channels.news.json", "channels/news/hello.json"} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s still on disk after channel remove", rel)
		}
	}
}
