# default/ — embedded wrapper templates for `drift atomic deploy`

These files are read at compile time via `//go:embed` directives in
`deploy.go` (deploy path) and `run.go` (local-dev path) and used to
generate the wrapper code injected into a user's function at build.
Nothing in here is read at runtime — every byte ships in the `drift`
binary.

**The SDK itself is no longer embedded, and the CLI is SDK-agnostic.**
Each function declares the SDK in its own manifest (`go.mod`,
`package.json`, `requirements.txt`, `Gemfile`, `composer.json`,
`Cargo.toml`); `drift atomic deploy` / `drift atomic run` just run the
package manager (`go mod tidy` / `npm` / `pip` / `bundler` / `composer` /
`cargo`) against whatever the manifest declares — no version is injected
or overridden. Only the thin wrapper/server templates that bridge the
runner's JSON wire protocol to user code live here.

## Filename convention

| Filename suffix | Why |
|---|---|
| `*.txt` | Default. Keeps Go's `go build`/`go vet` and other language tooling from picking the file up as source code in *this* package (the wrappers are Go/Python/Node/Ruby/PHP/Rust snippets, not part of the CLI's build). |
| `cargo_template.toml` | Plain `.toml`; no Go tooling claims it, and editors render it usefully. |

## What each file is

| File | Purpose |
|---|---|
| `server_get.txt` / `server_post.txt` | Local-dev Go server template (used by `drift atomic run`) — binds an HTTP listener for testing. |
| `server_get_native.txt` / `server_post_native.txt` | Deploy-time Go `main.go` template (reads the request off stdin, calls the user's handler, writes the envelope to stdout). |
| `wrapper_get_python.txt` / `wrapper_post_python.txt` | Python wrapper that imports the user's handler and serves it via the SDK's `run()` entry point. Placeholders: `{{SOURCE}}`, `{{FUNC}}`, `{{PORT}}`. |
| `wrapper_get_node.txt` / `wrapper_post_node.txt` | Same shape for Node.js (`require('@ondrift/sdk')`). |
| `wrapper_get_ruby.txt` / `wrapper_post_ruby.txt` | Same shape for Ruby (loads the bundled SDK via `bundler/setup`). |
| `wrapper_get_php.txt` / `wrapper_post_php.txt` | Same shape for PHP (Composer autoloads the SDK's `\Drift\` namespace). |
| `wrapper_get_rust.txt` / `wrapper_post_rust.txt` | Same shape for Rust (`drift_sdk::run`). |
| `cargo_template.toml` | `Cargo.toml` skeleton written only when a Rust function ships no `Cargo.toml` of its own. It declares a default `drift-sdk` git dependency so a bare function still builds; a function with its own `Cargo.toml` controls the version. |

The SDK version is the function author's choice, declared in the
function's manifest — the CLI does not pin or override it.
