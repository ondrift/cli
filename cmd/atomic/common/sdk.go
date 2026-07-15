// sdk.go — the one piece of SDK knowledge the CLI can't avoid.
//
// The CLI does NOT pin an SDK version anywhere. But the Go builder must
// name the SDK's ROOT module path when it runs `go get …@latest`, because
// the repo's early history contained a nested `github.com/ondrift/sdk/go`
// module. Its pseudo-versions still live on the Go proxy (immutable), so a
// bare `go mod tidy` on a function that imports `…/sdk/go` resolves one of
// those STALE commits instead of the current root module. Naming the root
// module disambiguates and pulls the latest real tag.
//
// This is a path, not a version — `@latest` means new SDK tags are picked
// up automatically with no CLI change... EXCEPT across a Go major version.
// Go's own semantic import versioning bakes the major into the module path
// itself (v2+ is "github.com/ondrift/sdk/vN", not a bare version bump —
// this repo is currently at /v3), so a future SDK major bump requires a
// matching CLI change here AND in every generated-wrapper template that
// hardcodes the same import path: cmd/atomic/cmd/deploy/default/
// server_{post,get}_native.txt, cmd/atomic/cmd/new/languages/golang_{get,
// post}.txt, and cmd/atomic/cmd/run/default/server_{post,get}.txt — not
// something `@latest` alone can carry across for Go specifically.
package atomic_common

// DriftGoModule is the root module path of the published Drift SDK.
const DriftGoModule = "github.com/ondrift/sdk/v3"
