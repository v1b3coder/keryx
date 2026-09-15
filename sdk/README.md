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
| `init` | operator | master + ops keys, full 4-role repo, anchor + repo |
| `keys list \| generate <name> \| export \| import` | operator | role-tagged, encrypted bundle (`--role`/`--name`/`--keyid` filters) |
| `channel add <name>` | operator | authored by default (`--simple` opts out) |
| `channel remove <name>` | operator | drops delegation + role metadata |
| `channel mode <name> simple\|authored` | operator | re-signs the channel's items |
| `channel set --channel <name>` | operator | display name / description |
| `channel list` | anyone | |
| `channel key rotate <name>` | operator | overlap (old+new), `--announce-next-key` pre-announces |
| `channel key revoke <name> --keyid <id>` | operator | drops a channel key; `--reissue` revokes and replaces in one update |
| `author add\|revoke\|list` | operator | refuses the last author |
| `pattern add\|remove` | operator | private-feed authorization |
| `company set` | operator | identity ceremony (name / logo) |
| `item sign --file draft.json --out signed.json` | author | OLPC + Ed25519, no repo |
| `item unpublish --channel <name> --id <id>` | pipeline | absence = unpublished |
| `publish --channel <name> --file signed.json` | pipeline | verifies the authors threshold |
| `refresh-timestamp` | ops/cron | the one cron line |
| `rotate-root [--announce-next-key]` | operator | anchor only |
| `validate` | anyone | full chain check |
| `join-url`, `qr` | operator | payload + QR (PNG) |
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
(or `KERYX_PASSPHRASE`) decrypts the keystore.

## Role-scoped workspaces

The repo is the only shared state (design/tooling.md §3.2). A workspace is
`keryx.json` + `repo/` + `anchor/` + `keys/`; each machine holds only its
own keys. A role that does not hold a key fails fast with a typed
`missing key: channel key "security" — run this on the pipeline machine`
error instead of producing half-signed metadata.

## Demo generator

`cmd/demo` generates a complete demonstration artifact (anchor + repo + keys +
join URL + QR + private capability feed) with the SDK:

```sh
go run ./cmd/demo --out /tmp/keryx-demo --base http://localhost:8000
```

The app's protocol tests consume it directly:

```sh
cd ../app
KERYX_DEMO_DIR=/tmp/keryx-demo npm test
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
cmd/demo/   demonstration artifact generator
```
