// digest.go — content-addressed fingerprinting for atomic functions.
//
// `drift project deploy` skips functions whose source hasn't changed. To do
// that safely it needs a stable fingerprint of a function's deploy inputs that
// it can compare against the digest the platform recorded at the last deploy.
//
// The digest is computed entirely client-side; the platform stores and returns
// it as an opaque token (see core/common/db/atomic.go). Everything that affects
// the deployed artefact lives in the hashed bytes: the @atomic directive,
// trigger/schedule comments, handler code, and dependency manifests are all in
// the source files; the element grouping (which comes from the Driftfile, not
// the source) is folded in explicitly.
package atomic_cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
	"github.com/ondrift/cli/v2/common"
)

// digestVersion is mixed into every digest. Bump it whenever the algorithm
// changes, so a changed algorithm can never read as "unchanged" against
// digests written by an older CLI — every function re-deploys exactly once.
const digestVersion = "drift-fn-digest-v1"

// digestSkipDirs are build/runtime artefact directories that must never
// influence a digest: they're regenerated on every build (or by local `drift
// atomic run`) and would otherwise make an unchanged function read as changed.
var digestSkipDirs = map[string]struct{}{
	"node_modules": {},
	"target":       {},
	"__pycache__":  {},
	"dist":         {},
	"build":        {},
	".venv":        {},
	"venv":         {},
	"vendor":       {},
}

// FunctionDigest returns a deterministic, mtime-independent fingerprint of the
// function at dir: every non-hidden source file (relative path + executable bit
// + content, in sorted order) plus the element. Identical trees always hash to
// the same value; any meaningful change flips it.
func FunctionDigest(dir, element string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve function dir: %w", err)
	}

	var rels []string
	walkErr := filepath.Walk(absDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(absDir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if info.IsDir() {
			if strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			if _, skip := digestSkipDirs[base]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(base, ".") {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("walk function dir: %w", walkErr)
	}
	sort.Strings(rels)

	h := sha256.New()
	// Version + element header. NUL delimiters everywhere so distinct inputs
	// can never concatenate into the same byte stream.
	fmt.Fprintf(h, "%s\x00element=%s\x00", digestVersion, element)

	for _, rel := range rels {
		full := filepath.Join(absDir, rel)
		info, serr := os.Stat(full)
		if serr != nil {
			return "", serr
		}
		mode := "0"
		if info.Mode()&0o111 != 0 {
			mode = "1" // executable bit can change runtime behaviour
		}
		fmt.Fprintf(h, "%s\x00%s\x00", filepath.ToSlash(rel), mode)
		f, oerr := os.Open(full) // #nosec G304 — hashing the user's own source by design
		if oerr != nil {
			return "", oerr
		}
		if _, cerr := io.Copy(h, f); cerr != nil {
			f.Close() // #nosec G104 -- discarded: copy already failed, this is cleanup
			return "", cerr
		}
		f.Close() // #nosec G104 -- discarded return is intentional; read path, nothing to flush
		fmt.Fprint(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ElementDigest fingerprints an element's package using only the TOP-LEVEL
// non-hidden files in dir — never subdirectories. For a Default element at
// atomic/, the subdirectories are OTHER elements, not part of this package, so
// folding them in would spuriously rebuild the Default when a sibling element
// changes. This matches the build's flat copy (copyGoSourceFiles). The digest
// is element-granular: any source change reflips it, so the whole element
// redeploys (its functions all share the same compiled package). `name` is
// mixed in exactly like FunctionDigest's element label.
func ElementDigest(dir, name string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve element dir: %w", err)
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return "", fmt.Errorf("read element dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	h := sha256.New()
	fmt.Fprintf(h, "%s\x00element=%s\x00", digestVersion, name)
	for _, n := range names {
		full := filepath.Join(absDir, n)
		info, serr := os.Stat(full)
		if serr != nil {
			return "", serr
		}
		mode := "0"
		if info.Mode()&0o111 != 0 {
			mode = "1"
		}
		fmt.Fprintf(h, "%s\x00%s\x00", n, mode)
		f, oerr := os.Open(full) // #nosec G304 — hashing the user's own source by design
		if oerr != nil {
			return "", oerr
		}
		if _, cerr := io.Copy(h, f); cerr != nil {
			f.Close() // #nosec G104
			return "", cerr
		}
		f.Close() // #nosec G104
		fmt.Fprint(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// deployedAtomic is the subset of an /ops/atomic/list record needed to match a
// local function against its last-deployed digest.
type deployedAtomic struct {
	FunctionName string `json:"function_name"`
	Method       string `json:"method"`
	Digest       string `json:"digest"`
}

// deployedKey is the identity a deployed record is filed under in the digest
// map — it must equal ElementFunc.DeployKey() for the local function. For HTTP
// that's "<method>:<path>" (so get:x and post:x are distinct); a queue handler's
// stored method is "queue", so its key stays the queue name (the function_name).
func deployedKey(method, functionName string) string {
	if method != "" && method != "queue" {
		return method + ":" + functionName
	}
	return functionName
}

// deployedDigestsTimeout bounds the best-effort "what's already deployed?"
// pre-check. It exists only to SKIP unchanged functions, so it must never
// cost more than it saves: if the list endpoint is slow, we give up quickly
// and deploy everything (the caller treats any error as "skip nothing")
// rather than stalling the whole deploy on the default 30s client timeout.
const deployedDigestsTimeout = 8 * time.Second

// DeployedDigests returns function_name -> last-deployed source digest for the
// active slice. `drift project deploy` uses it to skip functions whose source
// is unchanged. Records with no recorded digest (deployed by an older CLI, or
// after a rollback / snapshot restore) are omitted, so they always redeploy.
func DeployedDigests() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deployedDigestsTimeout)
	defer cancel()

	resp, err := common.DoRequestWithContext(ctx, http.MethodGet, common.APIBaseURL+"/ops/atomic/list", nil)
	if err != nil {
		return nil, common.TransportError("list atomic functions", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "list atomic functions")
	if err != nil {
		return nil, err
	}

	var records []deployedAtomic
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("list atomic functions: unexpected response (%w)", err)
	}

	out := make(map[string]string, len(records))
	for _, r := range records {
		if r.FunctionName != "" && r.Digest != "" {
			out[deployedKey(r.Method, r.FunctionName)] = r.Digest
		}
	}
	return out, nil
}

// FunctionName returns the deploy identity for the function at dir — the key
// under which the last-deployed digest is looked up and the legacy route
// collision is checked. For HTTP that's "<method>:<path>" (so get:x and post:x
// are distinct functions); for a queue trigger it's the directory basename
// (mirroring DeployFolder).
func FunctionName(dir string) (string, error) {
	meta, err := atomic_common.ParseAtomicMetadataFromDir(dir)
	if err != nil {
		return "", err
	}
	if meta.Trigger == "queue" {
		abs, aerr := filepath.Abs(dir)
		if aerr != nil {
			return "", aerr
		}
		return filepath.Base(abs), nil
	}
	return deployedKey(meta.Method, meta.Path), nil
}
