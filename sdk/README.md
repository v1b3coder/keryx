# Keryx publisher SDK + `pub` CLI

Reference publisher tooling for the Keryx protocol: a Go SDK
(`github.com/v1b3coder/keryx/sdk`) plus the `pub` CLI that consumes it.
Normative rules live in [`../spec/`](../spec/); the product view is
[`../design/tooling.md`](../design/tooling.md).

The SDK is the only implementation of the publisher side; the CLI is a thin
front end (flags, prompts, rendering, exit codes). The future web publishing
app consumes the same SDK — every operation takes a `context.Context`, returns
values, and never writes to stdout.

## Build

```sh
cd sdk
go build ./...
go test ./...
go run ./cmd/pub --help
```

## Quick start (single machine)

```sh
pub init --domain company.example --name "ACME s.r.o." --base https://cdn.example.com/keryx
pub channel add marketing --display-name Offers          # authored by default
pub channel add status --simple                          # explicit simple mode

# author side (author key only, no repo needed)
pub item sign --file draft.json --out signed.json

# pipeline
pub publish --channel marketing --file signed.json
pub refresh-timestamp
pub validate

pub join-url --channels marketing,status --private-feed "https://eshop.example.com/channels/tracking/<token>/feed.json"
pub qr --channels marketing,status --out qr.png
```

Output is two directories (defaults under `--workspace .keryx`):

- `anchor/` — the well-known root anchor (`root.json` + every `N.root.json`),
  upload to `https://<join-origin>/.well-known/keryx/`
- `repo/` — the repo base (no root metadata), upload to `custom.repo_base`

## Commands

| Command | Role | Notes |
|---|---|---|
| `init` | operator | master + ops keys, full 4-role repo, anchor + repo; remembers the join origin |
| `keys list \| generate <name> \| export \| import` | operator | role-tagged, encrypted bundle (`--role`/`--name`/`--keyid` filters) |
| `channel add <name>` | operator | authored by default (`--simple` opts out) |
| `channel remove <name>` | operator | drops delegation + role metadata |
| `channel mode <name> simple\|authored` | operator | re-signs the channel's items (`--channel` also accepted) |
| `channel set --channel <name>` | operator | display name / description |
| `channel list` | anyone | |
| `channel key rotate <name>` / `rotate --channel <name>` | operator | overlap (old+new), `--announce-next-key` pre-announces |
| `channel key revoke <name> --keyid <id>` / `revoke --channel <name>` | operator | drops a channel key; `--reissue` revokes and replaces in one update |
| `author add\|revoke\|list` | operator | refuses the last author |
| `pattern add\|remove` | operator | private-feed authorization |
| `company set` | operator | identity ceremony (name / logo) |
| `item sign --file draft.json --out signed.json` | author | OLPC + Ed25519, no repo |
| `item unpublish --channel <name> --id <id>` | pipeline | absence = unpublished |
| `publish --channel <name> --file signed.json` | pipeline | verifies the authors threshold |
| `refresh-timestamp` | ops/cron | the one cron line (`--expires` overrides the TTL) |
| `rotate-root [--announce-next-key]` | operator | anchor only |
| `validate` | anyone | full chain check |
| `join-url`, `qr` | operator | payload + QR (PNG); default origin is the `init --domain` join origin |
| `pull [--base URL]` | CI/ops | fetch + verify without git |
| `deploy local --target DIR` / `deploy s3` | deployer | thin `Deployer` interface |
| `private-feed new\|update\|expire` | engine | capability-feed documents |

Every master ceremony accepts `--stage <dir>` to run the strict two-step
ceremony (design/tooling.md §3.5): the offline machine writes a
master-signed `targets.json` plus the new keys and a step manifest, and a
CI/ops machine finishes it with `pub ceremony apply --bundle <dir>`. Without
`--stage` the same command runs single-step when the keystore holds every
required key.

`--json` renders machine-readable results for every command; `--passphrase`
(or `KERYX_PASSPHRASE`) decrypts the keystore. `--keystore <dir>`
(or `KERYX_KEYSTORE`) points at the key store; it defaults to a per-user
location (`~/.local/share/keryx/keys`) and is never written into the
workspace. Master ceremonies that mint keys (`channel add`, `channel mode`)
require `--generate-keys`; without it a missing key is a typed error.

## Key store and resolution

The key store lives outside the repo, so private seeds can never be
committed with it. Signing resolves keys in two ways
(design/tooling.md §3.2):

- **Pinned** — metadata already names the keyid (`root.json`/
  `targets.json` roles, or an explicit `--keyid`): the keyid is looked up
exactly, never by name.
- **Selection** — only a role is known (`item sign`, a new channel key):
  exactly one key of that role is picked; zero is a typed `missing key`
  error and more than one is an ambiguity error listing the candidates.

`pub keys export`/`import` moves role-tagged keys between machines as an
encrypted bundle.

## Role-scoped workspaces

The repo is the only shared state (design/tooling.md §3.2). A workspace is
`keryx.json` + `repo/` + `anchor/`; the key store is a sibling directory
(or `$KERYX_KEYSTORE`) that each machine keeps to itself. A role that does
not hold a key fails fast with a typed
`missing key: channel key "security" — run this on the pipeline machine`
error instead of producing half-signed metadata, and the workspace `role`
(`operator|ci|author`) gates which commands it may run.

## Showcase consumer

The `examples/` module is a separate consumer of this SDK — the same shape an
external project would have (its own `go.mod` with a `replace` to `../sdk`).
`examples/sdk-artifact` generates a minimal demonstration artifact (anchor +
repo + keys + join URL + QR + private capability feed):

```sh
cd ../examples && go run ./sdk-artifact --out /tmp/keryx-demo --keys /tmp/keryx-keys --base http://localhost:8000
```

The app's protocol tests consume it directly (`make demo-sdk`):

```sh
cd ../app
KERYX_DEMO_DIR=/tmp/keryx-demo KERYX_KEYSTORE=/tmp/keryx-keys npm test
```

## Layout

```
keys/       Ed25519 keys, TUF keyids, role-tagged store, export/import
repo/       Repo interface + DirRepo (object storage / DB later)
feed/       item types, OLPC sign/verify, private capability documents
tufrepo/    full repo build/load/verify via go-tuf v2
publisher/  one method per CLI verb (Init, Publish, ceremonies, Validate, …)
ceremony/   stage/apply bundle types + key encryption
join/       join URL + QR payload
deploy/     Deployer: local, S3-compatible
config/     role-scoped workspace layout
cmd/pub/    the cobra CLI
```

The SDK module contains no demo or sample content; the showcase consumers
live in the top-level `examples/` module (and the full demo site generator in
`demo-tool/`).
