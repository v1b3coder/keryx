package publisher_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/v1b3coder/keryx/sdk/publisher"
	"github.com/v1b3coder/keryx/sdk/repo"
)

// TestValidateRejectsCorruptRepository is the publisher-side counterpart of the
// TUF conformance suite (theupdateframework/tuf-conformance): it takes a valid
// repository and applies one corruption at a time, asserting that Validate rejects
// every one. A repository that passes validation but fails on a client is exactly the
// failure mode this guards against (the client would suspend or drop content).
func TestValidateRejectsCorruptRepository(t *testing.T) {
	type corruptCase struct {
		name   string
		mutate func(t *testing.T, base, anchor string)
	}
	cases := []corruptCase{
		{"targets custom field edited", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "targets.json"), func(doc map[string]any) {
				doc["signed"].(map[string]any)["custom"].(map[string]any)["company_name"] = "Evil Corp"
			})
		}},
		{"targets delegation key object removed", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "targets.json"), func(doc map[string]any) {
				keys := doc["signed"].(map[string]any)["delegations"].(map[string]any)["keys"].(map[string]any)
				for k := range keys {
					delete(keys, k)
					break
				}
			})
		}},
		{"channel role target hash changed", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "channels.news.json"), func(doc map[string]any) {
				targets := doc["signed"].(map[string]any)["targets"].(map[string]any)
				for path := range targets {
					entry := targets[path].(map[string]any)
					entry["length"] = float64(1)
					break
				}
			})
		}},
		{"channel role metadata removed", func(t *testing.T, base, _ string) {
			if err := os.Remove(filepath.Join(base, "channels.news.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"snapshot references a wrong targets version", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "snapshot.json"), func(doc map[string]any) {
				meta := doc["signed"].(map[string]any)["meta"].(map[string]any)
				meta["targets.json"].(map[string]any)["version"] = float64(99)
			})
		}},
		{"snapshot drops a channel role", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "snapshot.json"), func(doc map[string]any) {
				meta := doc["signed"].(map[string]any)["meta"].(map[string]any)
				delete(meta, "channels.news.json")
			})
		}},
		{"timestamp references a wrong snapshot version", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "timestamp.json"), func(doc map[string]any) {
				meta := doc["signed"].(map[string]any)["meta"].(map[string]any)
				meta["snapshot.json"].(map[string]any)["version"] = float64(99)
			})
		}},
		{"item bytes tampered", func(t *testing.T, base, _ string) {
			path := filepath.Join(base, "channels", "news", "hello.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)/2] ^= 0xff
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"item id no longer matches its path segment", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "channels", "news", "hello.json"), func(doc map[string]any) {
				doc["id"] = "other"
			})
		}},
		{"item author signature removed", func(t *testing.T, base, _ string) {
			editJSON(t, filepath.Join(base, "channels", "security", "hello.json"), func(doc map[string]any) {
				doc["sig"] = []any{}
			})
		}},
		{"root anchor tampered", func(t *testing.T, _, anchor string) {
			editJSON(t, filepath.Join(anchor, "root.json"), func(doc map[string]any) {
				doc["signed"].(map[string]any)["custom"].(map[string]any)["mode"] = "lite"
			})
		}},
		{"released root version removed", func(t *testing.T, _, anchor string) {
			// a rotated repo whose 1.root.json is gone cannot be chained
			if err := os.Remove(filepath.Join(anchor, "1.root.json")); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := e.ctx()
			// a valid repo with an authored channel (two authors), a simple
			// channel and a published item in each
			if _, err := e.pub.ChannelAdd(ctx, publisher.ChannelSpec{
				Name: "security", Threshold: 1,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.pub.ChannelAdd(ctx, publisher.ChannelSpec{Name: "news", Simple: true}); err != nil {
				t.Fatal(err)
			}
			authored := e.signDraft("security", draft("hello"))
			if _, err := e.pub.Publish(ctx, publisher.PublishParams{Channel: "security", Item: authored}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.pub.Publish(ctx, publisher.PublishParams{Channel: "news", Item: draft("hello")}); err != nil {
				t.Fatal(err)
			}
			// rotate once so the anchor carries a real 1.root.json chain
			if _, err := e.pub.RotateRoot(ctx, false); err != nil {
				t.Fatal(err)
			}
			if _, err := e.pub.Validate(ctx); err != nil {
				t.Fatalf("baseline validate: %v", err)
			}

			base := e.pub.Base.(*repo.DirRepo).Root
			anchor := e.pub.Anchor.(*repo.DirRepo).Root
			tc.mutate(t, base, anchor)

			if _, err := e.pub.Validate(ctx); err == nil {
				t.Fatalf("Validate accepted a corrupted repository: %s", tc.name)
			}
		})
	}
}

// editJSON applies fn to a JSON object file in place.
func editJSON(t *testing.T, path string, fn func(doc map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}
