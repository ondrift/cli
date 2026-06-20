# Drift CLI

`drift` is the command-line client for [Drift](https://ondrift.eu) — a simple, European serverless cloud. Everything you do on Drift happens through this one tool: create your environment, deploy serverless functions in six languages, host static sites, store data, and ship an entire application with a single command. There is no web dashboard — the browser opens exactly once, to configure a slice.

A **slice** is your isolated environment. It bundles three primitives:

- **Atomic** — serverless functions in Go, Python, Node.js, Ruby, PHP, and Rust.
- **Backbone** — your data: secrets, a NoSQL store, queues, blobs, a cache, and locks.
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

### Account

| Command | Description |
|---------|-------------|
| `drift account create` | Sign up — username, email, password, email OTP verification. |
| `drift account login` | Authenticate; stores a session token in `~/.drift/session.json`. |

### Slices

| Command | Description |
|---------|-------------|
| `drift slice create [name]` | Create a slice (opens the configurator in your browser; `--headless` for a free slice). |
| `drift slice list` | List your slices; the active one is marked. |
| `drift slice use <name>` | Set the active slice for subsequent commands. |
| `drift slice plan` | Show resource usage vs. limits for the active slice. |
| `drift slice resize <name>` | Reconfigure resources (opens the configurator). |
| `drift slice restart` | Restart the active slice. |
| `drift slice delete <name>` | Delete a slice (double confirmation). |
| `drift slice domain add\|verify\|remove\|list <host>` | Attach and verify custom domains. |
| `drift slice auth set\|list\|disable` | Put a username/password login in front of your site — a soft gate for not-public-yet sites (covers site pages, not `/api`). |
| `drift slice snapshot create\|list\|download\|restore\|delete` | Portable backups — your data, with no Drift-specific files. |

### Atomic — serverless functions

| Command | Description |
|---------|-------------|
| `drift atomic new [name]` | Scaffold a function in any of the six languages, HTTP or queue trigger. Flags: `-l/--lang`, `-m/--method`, `-q/--queue`, `-a/--auth`. |
| `drift atomic fetch [path]` | Resolve dependencies for every function found under a path. |
| `drift atomic run <dir>` | Run a function locally with hot reload. |
| `drift atomic deploy <dir>` | Build, archive, and deploy a function. |
| `drift atomic list` | List deployed functions. |
| `drift atomic logs <name>` | View a function's logs (`logs purge` clears them). |
| `drift atomic metrics <name>` | Request count, error rate, average duration. |
| `drift atomic redeploy\|rollback\|history <name>` | Manage deployed versions. |
| `drift atomic trigger\|alert\|egress\|auth <name>` | Configure triggers, alerts, outbound egress, and API-key auth. |
| `drift atomic delete <name>` | Remove a function. |

### Backbone — data

| Command | Description |
|---------|-------------|
| `drift backbone secret set\|get\|delete` | Encrypted secrets (the key never reaches your code). |
| `drift backbone nosql write\|read\|list\|delete\|drop` | NoSQL collections. |
| `drift backbone queue push\|pop\|peek\|len` | Queues. |
| `drift backbone blob put\|get\|list\|delete` | Blob storage. |
| `drift backbone cache set\|get\|del\|exists` | Ephemeral cache. |
| `drift backbone lock acquire\|release` | Distributed locks. |

### Canvas — static sites

| Command | Description |
|---------|-------------|
| `drift canvas deploy <dir>` | Deploy a static site, served same-origin with your functions. |

### Projects

| Command | Description |
|---------|-------------|
| `drift project deploy` | Deploy an entire application from its `Driftfile`. Functions whose source is unchanged since the last deploy are skipped automatically; pass `--force` to redeploy everything. |
| `drift project diff` | Preview what a deploy would change (never shrinks a slice). |
| `drift plan <Driftfile>` | Estimate the monthly price of a configuration. |

## Configuration

| Variable | Purpose |
|----------|---------|
| `NO_COLOR` | Disable coloured output. |

Session tokens live in `~/.drift/session.json`.

## License

MIT — see [LICENSE](LICENSE).
