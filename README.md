# Drift CLI

`drift` is the command-line client for [Drift](https://ondrift.eu) — a simple, European serverless cloud. Everything you do on Drift happens through this one tool: create your environment, deploy serverless functions in six languages, host static sites, store data, and ship an entire application with a single command. There is no web dashboard — running `drift` on its own opens a full-screen terminal dashboard (TUI) over your slices, functions, and data; the browser opens at most once, to configure a slice.

A **slice** is your isolated environment. It bundles three primitives:

- **Atomic** — serverless functions in Go, Python, Node.js, Ruby, PHP, and Rust.
- **Backbone** — your data and state: secrets, a NoSQL store, SQL databases, queues, blobs, a cache, locks, realtime channels, passwordless auth (KeyAuth), and a zero-knowledge vault.
- **Canvas** — static site hosting, served same-origin with your functions (no CORS).

## Install

```bash
go install github.com/ondrift/cli/cmd/drift@latest
```

This installs the `drift` binary into your `$GOBIN`. Or build from source:

```bash
git clone https://github.com/ondrift/cli && cd cli
go build -o drift ./cmd/drift
```

## Getting started

```bash
drift account create                   # sign up (email verification)
drift slice create my-app              # create a slice (opens the configurator)
drift slice use my-app                 # make it the active slice
drift atomic new hello -l go -m get    # scaffold a function
drift atomic deploy ./hello            # deploy it
drift project deploy                   # …or deploy a whole app from a Driftfile
```

## Commands

### Dashboard

| Command | Description |
|---------|-------------|
| `drift` | With no arguments in a terminal, launches the full-screen interactive dashboard (a btop/k9s-style TUI) over your slices, functions, and Backbone data. In a pipe or CI (no TTY) it falls back to `--help`. |

### Account

| Command | Description |
|---------|-------------|
| `drift account create` | Sign up — username, email, password, email OTP verification. |
| `drift account login` | Authenticate; stores a session token in `~/.drift/session.json`. |
| `drift upgrade [version]` | Update the CLI via `go install` — latest by default, or pin a version (e.g. `drift upgrade v1.8.1`) to roll back. The dashboard nudges you when a newer release exists. |

### Slices

| Command | Description |
|---------|-------------|
| `drift slice create [name]` | Create a slice (opens the configurator in your browser; `--headless` for a free slice). |
| `drift slice list` | List your slices; the active one is marked. |
| `drift slice use <name>` | Set the active slice for subsequent commands. |
| `drift slice resize [name]` | Reconfigure resources for a slice (defaults to the active slice; opens the configurator, or `--from <Driftfile>` for a headless resize). |
| `drift slice restart` | Restart the active slice. |
| `drift slice delete <name>` | Delete a slice (double confirmation). |
| `drift slice domain add\|verify\|remove\|list <host>` | Attach and verify custom domains. |
| `drift slice link add\|list\|remove <slice>` | Let the active slice call another slice you own, in-cluster (e.g. an app → your own observability slice). Use it from code with `drift.Slice("<slice>")`. |
| `drift slice snapshot create\|list\|download\|restore\|delete` | Portable backups — your data, with no Drift-specific files. |

### Atomic — serverless functions

| Command | Description |
|---------|-------------|
| `drift atomic new [name]` | Add a function to an element &mdash; a flat file under `atomic/`, not a per-function folder. HTTP or queue trigger. Flags: `-l/--lang`, `-m/--method`, `-q/--queue`, `-a/--auth`, `-e/--element`. |
| `drift atomic fetch [path]` | Resolve dependencies for every function found under a path. |
| `drift atomic run <dir>` | Run a function locally with hot reload. |
| `drift atomic deploy <dir>` | Build, archive, and deploy a function. |
| `drift atomic list` | List deployed functions. |
| `drift atomic logs <name>` | View a function's logs (`logs purge` clears them). |
| `drift atomic metrics <name>` | Request count, error rate, average duration. |
| `drift atomic redeploy\|rollback\|history <name>` | Manage deployed versions. |
| `drift atomic trigger\|alert\|egress\|auth <name>` | Configure triggers, alerts, outbound egress, and API-key auth. |
| `drift atomic element list` | List your elements (single-language backends) and the functions each contains. |
| `drift atomic delete <name>` | Remove a function. |

### Backbone — data

| Command | Description |
|---------|-------------|
| `drift backbone secret set\|get\|delete` | Encrypted secrets (the key never reaches your code). |
| `drift backbone nosql write\|read\|list\|drop` | NoSQL collections. |
| `drift backbone sql list\|drop` | Per-slice SQLite databases. |
| `drift backbone queue push\|pop\|peek\|len\|drop` | Queues. |
| `drift backbone blob put\|get\|list\|delete` | Blob storage. |
| `drift backbone cache set\|get\|del\|exists` | Ephemeral cache. |
| `drift backbone lock acquire\|release\|renew` | Distributed locks. |
| `drift backbone status` | Backbone health and resource usage for the active slice. |

### Canvas — static sites

| Command | Description |
|---------|-------------|
| `drift canvas deploy <dir>` | Deploy a static site, served same-origin with your functions. |

### Projects

| Command | Description |
|---------|-------------|
| `drift project deploy [env]` | Deploy an entire application from its `Driftfile`. Functions whose source is unchanged since the last deploy are skipped automatically; pass `--force` to redeploy everything. |
| `drift project run [env]` | Build and run the whole project locally in Docker — no account, no cloud. The "run it locally" half of the two-button story. |
| `drift project stop [env]` | Stop the local run. |
| `drift project logs [env]` | Tail logs from the local run. |
| `drift project diff` | Preview what a deploy would change (never shrinks a slice). |

### Migrate — leave another cloud

Free, read-only, and honest about the parts that don't fit. Drift never holds your
cloud credentials or your data — every command runs locally with your own provider
login. The first provider is Azure.

| Command | Description |
|---------|-------------|
| `drift migrate azure estimate -g <rg>` | Read-only: map an Azure resource group's cost to the equivalent Drift slice. |
| `drift migrate azure snapshot -g <rg> -o <dir>` | Read-only: pull function source (Python/Node, incl. Linux Consumption), Cosmos Mongo documents, blobs, queue messages (peeked, never dequeued), `$web` static sites, and app settings into a vendor-neutral folder you own. |
| `drift migrate azure transform -i <dir> -o <dir>` | Offline, deterministic: rewrite the export into a deployable Drift project (validated `Driftfile` + scaffolds). |
| `drift migrate azure apply -i <dir>` | Deploy the workspace (`-i`/`--in`, default `./drift_workspace`); hard-refuses around anything in `REFUSED.md` unless `--accept-refusals`. |

Anything that can't move is written to `REFUSED.md` with a reason; review it and
`REPORT.md` before deploying. Cosmos export needs `mongoexport`; Linux Consumption
source needs `squashfs-tools` — the tool prints the exact install if either is missing.

## Configuration

| Variable | Purpose |
|----------|---------|
| `NO_COLOR` | Disable coloured output. |

Session tokens live in `~/.drift/session.json`.

## License

MIT — see [LICENSE](LICENSE).
