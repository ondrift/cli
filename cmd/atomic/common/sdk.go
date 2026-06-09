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
// up automatically with no CLI change.
package atomic_common

// DriftGoModule is the root module path of the published Drift SDK.
const DriftGoModule = "github.com/ondrift/sdk"
