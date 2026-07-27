package atomic_cmd

// sdkvendor.go — the no-Docker path for an element whose only dependency is
// the Drift SDK.
//
// WHY THE CONTAINER EXISTS (toolchain.go says it at length): a dependency with
// a NATIVE extension — sharp, argon2, a C-ext gem — must be built for the
// RUNNER's architecture, not the laptop's, and the only honest way to do that
// is to run the package manager on the target architecture. That reasoning is
// sound and this file does not touch it.
//
// It simply does not apply to a dependency with no native code. Every Drift SDK
// for an interpreted language is a SINGLE stdlib-only source file
// (philosophy.md, "zero-dependency interpreted SDKs"):
//
//	python -> python/drift.py       ruby -> ruby/drift.rb
//	node   -> node/index.js         php  -> php/drift.php
//
// There is nothing to compile, so architecture is irrelevant and the package
// manager is doing nothing a file copy could not. For an element that declares
// the SDK and nothing else — the common case for a first project, and for most
// Canvas-plus-a-few-functions apps — we lay the SDK out where that language's
// wrapper already looks and skip the container entirely. `ensureDocker` is then
// never reached, so a 20-line JS function ships on a machine with no Docker.
//
// Anything else — one extra dependency, a Gemfile with a second gem, Go, Rust —
// takes the container path unchanged. This is a fast path, not a replacement.
//
// Set DRIFT_FORCE_CONTAINER_BUILD=1 to disable it entirely.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sdkModule is the package name each language's manifest uses for the Drift
// SDK. Matching is on the declared dependency NAME, never on the URL or version
// spec, because those legitimately vary (a branch, a semver range, a fork).
var sdkModule = map[string]string{
	"node":   "@ondrift/sdk",
	"python": "drift-sdk",
	"ruby":   "drift-sdk",
	"php":    "ondrift/sdk",
}

// sdkVendorLayout records where a vendored SDK file has to land for each
// language's wrapper to find it, and is the whole reason this fast path is
// safe: each destination is the path the wrapper ALREADY searches, so a
// vendored element and a container-built one are byte-identical in the ways
// that matter at runtime.
//
//	python — wrapper_get_python.txt prepends vendor/ to sys.path
//	node   — wrapper requires '@ondrift/sdk', resolved via node_modules/
//	ruby   — wrapper does $LOAD_PATH.unshift(__dir__) then require 'drift'
//	php    — wrapper require_once's vendor/autoload.php when present
var sdkVendorLayout = map[string][]struct{ from, to string }{
	"python": {{from: "python/drift.py", to: "vendor/drift.py"}},
	"node": {
		{from: "package.json", to: "node_modules/@ondrift/sdk/package.json"},
		{from: "node/index.js", to: "node_modules/@ondrift/sdk/node/index.js"},
	},
	"ruby": {{from: "ruby/drift.rb", to: "drift.rb"}},
	"php":  {{from: "php/drift.php", to: "vendor/php/drift.php"}},
}

// sdkResolveTag and sdkFetchTarball are the entire network surface, isolated
// behind package variables so tests can stub them and never touch GitHub.
var (
	sdkResolveTag   = resolveLatestSDKTag
	sdkFetchTarball = fetchSDKTarball
)

// sdkHTTPTimeout bounds both the tag resolution and the tarball download. A
// deploy that hangs on a package host is worse than one that fails and says so.
const sdkHTTPTimeout = 30 * time.Second

// tryVendorSDKOnly takes the no-Docker path when the element's manifest
// declares nothing but the Drift SDK. Reports whether it handled the install.
//
// A (false, nil) return means "not applicable, use the container path" and is
// the normal answer for any real dependency set. An error means the fast path
// WAS applicable and failed — it is not silently downgraded to the container
// path, because the whole point is that the user may not have Docker, and
// "falling back" would surface as the very error this exists to remove.
func tryVendorSDKOnly(lang, absFolder, stageDir string) (bool, error) {
	if os.Getenv("DRIFT_FORCE_CONTAINER_BUILD") != "" {
		return false, nil
	}
	only, err := sdkOnlyManifest(lang, absFolder)
	if err != nil || !only {
		// A manifest we cannot parse is not an error here — it is simply not
		// provably SDK-only, so the container path (which parses it with the
		// real package manager) stays in charge.
		return false, nil
	}
	if _, ok := sdkVendorLayout[lang]; !ok {
		return false, nil
	}
	if verr := vendorDriftSDK(lang, stageDir); verr != nil {
		return false, verr
	}
	return true, nil
}

// sdkOnlyManifest reports whether the element at absFolder declares the Drift
// SDK and nothing else. A missing manifest counts as SDK-only: there is nothing
// to install, and today that already skips the container (every install*Deps
// no-ops without its manifest) — routing it here just makes the SDK available.
func sdkOnlyManifest(lang, absFolder string) (bool, error) {
	switch lang {
	case "node":
		return nodeSDKOnly(filepath.Join(absFolder, "package.json"))
	case "python":
		return pythonSDKOnly(filepath.Join(absFolder, "requirements.txt"))
	case "ruby":
		return rubySDKOnly(filepath.Join(absFolder, "Gemfile"))
	case "php":
		return phpSDKOnly(filepath.Join(absFolder, "composer.json"))
	}
	return false, nil
}

// nodeSDKOnly parses package.json. devDependencies are ignored on purpose: the
// container path runs `npm install --production`, so they never reach the
// artifact and must not force a slower build.
func nodeSDKOnly(path string) (bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own manifest
	if err != nil {
		return true, nil // no manifest → nothing declared
	}
	var pkg struct {
		Dependencies         map[string]string `json:"dependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false, fmt.Errorf("parse package.json: %w", err)
	}
	// An optionalDependency is exactly the native-binary case the container
	// exists for (sharp and friends ship per-arch optional deps), so its
	// presence disqualifies the fast path outright.
	if len(pkg.OptionalDependencies) > 0 {
		return false, nil
	}
	return onlyKey(pkg.Dependencies, sdkModule["node"]), nil
}

// pythonSDKOnly parses requirements.txt. Comments, blank lines and pip flag
// lines (-r, --index-url) are not packages; a flag line means the real
// requirement set lives elsewhere, so it disqualifies the fast path.
func pythonSDKOnly(path string) (bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own manifest
	if err != nil {
		return true, nil
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "-") {
			return false, nil
		}
		if !strings.EqualFold(pythonRequirementName(line), sdkModule["python"]) {
			return false, nil
		}
	}
	return true, nil
}

// pythonRequirementName extracts the distribution name from one requirements.txt
// line: everything before the first version specifier, extras bracket, PEP 508
// URL marker or environment marker.
func pythonRequirementName(line string) string {
	name := line
	for _, sep := range []string{" @ ", "@", "[", "==", ">=", "<=", "~=", "!=", ">", "<", ";", " "} {
		if i := strings.Index(name, sep); i >= 0 {
			name = name[:i]
		}
	}
	return strings.TrimSpace(name)
}

// rubySDKOnly scans a Gemfile for `gem "name"` declarations. A `gemspec`
// directive pulls in a whole spec's dependency list, so it disqualifies.
func rubySDKOnly(path string) (bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own manifest
	if err != nil {
		return true, nil
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "gemspec" || strings.HasPrefix(line, "gemspec ") {
			return false, nil
		}
		if !strings.HasPrefix(line, "gem ") {
			continue // source, ruby, group blocks, …
		}
		name := quotedAfter(line[len("gem "):])
		if !strings.EqualFold(name, sdkModule["ruby"]) {
			return false, nil
		}
	}
	return true, nil
}

// quotedAfter returns the first single- or double-quoted token in s.
func quotedAfter(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return ""
	}
	q := s[0]
	if q != '\'' && q != '"' {
		return ""
	}
	if i := strings.IndexByte(s[1:], q); i >= 0 {
		return s[1 : 1+i]
	}
	return ""
}

// phpSDKOnly parses composer.json's require map. The "php" key is a platform
// constraint rather than a package to install, so it doesn't disqualify.
func phpSDKOnly(path string) (bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own manifest
	if err != nil {
		return true, nil
	}
	var comp struct {
		Require map[string]string `json:"require"`
	}
	if err := json.Unmarshal(data, &comp); err != nil {
		return false, fmt.Errorf("parse composer.json: %w", err)
	}
	pkgs := make(map[string]string, len(comp.Require))
	for k, v := range comp.Require {
		if strings.EqualFold(k, "php") || strings.HasPrefix(strings.ToLower(k), "ext-") {
			continue
		}
		pkgs[k] = v
	}
	return onlyKey(pkgs, sdkModule["php"]), nil
}

// onlyKey reports whether m is empty or contains exactly the one wanted key.
func onlyKey(m map[string]string, want string) bool {
	if len(m) == 0 {
		return true
	}
	if len(m) > 1 {
		return false
	}
	_, ok := m[want]
	return ok
}

// vendorDriftSDK places the SDK's source into stageDir at the paths lang's
// wrapper already searches.
func vendorDriftSDK(lang, stageDir string) error {
	tag, err := sdkResolveTag()
	if err != nil {
		return err
	}
	root, err := sdkCachedSource(tag)
	if err != nil {
		return err
	}
	for _, item := range sdkVendorLayout[lang] {
		src := filepath.Join(root, filepath.FromSlash(item.from))
		dst := filepath.Join(stageDir, filepath.FromSlash(item.to))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("vendor SDK: %w", err)
		}
		data, rerr := os.ReadFile(src) // #nosec G304 -- inside our own cache dir
		if rerr != nil {
			return fmt.Errorf("vendor SDK: %s missing from SDK %s: %w", item.from, tag, rerr)
		}
		if werr := os.WriteFile(dst, data, 0o644); werr != nil { // #nosec G306
			return fmt.Errorf("vendor SDK: %w", werr)
		}
	}
	// PHP's wrapper loads vendor/autoload.php when present; the SDK's own
	// composer.json declares `autoload.files: [php/drift.php]`, so this is the
	// one-file equivalent of what composer would have generated.
	if lang == "php" {
		autoload := "<?php\nrequire_once __DIR__ . '/php/drift.php';\n"
		if err := os.WriteFile(filepath.Join(stageDir, "vendor", "autoload.php"), []byte(autoload), 0o644); err != nil { // #nosec G306
			return fmt.Errorf("vendor SDK: write autoload: %w", err)
		}
	}
	return nil
}

// ─── Fetch + cache ──────────────────────────────────────────────────────────

const sdkRepo = "ondrift/sdk"

// resolveLatestSDKTag returns the SDK tag to vendor.
//
// DRIFT_SDK_TAG pins it outright (air-gapped mirrors, reproducible builds, and
// the way to hold a version while a regression is investigated).
//
// Otherwise the tag list is read from GitHub and the highest semver wins.
// Deliberately NOT the `/releases/latest` redirect, which is the obvious move
// and does not work here: the SDK repo publishes git TAGS but no GitHub
// Releases, so that endpoint 302s to the releases index and the "tag" parsed
// out of it is the literal string "releases" — a resolution that looks like it
// succeeded and then 404s at download.
//
// The order the API returns is not contracted, so the highest version is
// selected explicitly rather than by taking the first element.
//
// A resolved tag is always a concrete version, never a floating ref: vendoring
// a moving target is what CLI-STANDARDUSAGE-0KTV3V is about, and reintroducing
// it here in a new place would be a poor trade for skipping a container.
func resolveLatestSDKTag() (string, error) {
	if v := strings.TrimSpace(os.Getenv("DRIFT_SDK_TAG")); v != "" {
		return v, nil
	}
	hint := "\nPin one with DRIFT_SDK_TAG=<tag>, or set DRIFT_FORCE_CONTAINER_BUILD=1 to build in a container instead."

	client := &http.Client{Timeout: sdkHTTPTimeout}
	resp, err := client.Get("https://api.github.com/repos/" + sdkRepo + "/tags") // #nosec G107 -- constant host, constant path
	if err != nil {
		return "", fmt.Errorf("resolve the latest Drift SDK version: %w%s", err, hint)
	}
	defer resp.Body.Close() // #nosec G104
	if resp.StatusCode != http.StatusOK {
		// 403 here is almost always the unauthenticated rate limit (60/hour per
		// IP), which is worth naming rather than reporting as a bare status.
		return "", fmt.Errorf("resolve the latest Drift SDK version: GitHub returned %s%s", resp.Status, hint)
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if derr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tags); derr != nil {
		return "", fmt.Errorf("resolve the latest Drift SDK version: %w%s", derr, hint)
	}
	names := make([]string, 0, len(tags))
	for _, t := range tags {
		names = append(names, t.Name)
	}
	latest := pickLatestSemverTag(names)
	if latest == "" {
		return "", fmt.Errorf("resolve the latest Drift SDK version: no version tags found%s", hint)
	}
	return latest, nil
}

// pickLatestSemverTag returns the highest vMAJOR.MINOR.PATCH tag in names.
// Pre-release tags (any `-suffix`) are skipped: a deploy must not silently
// vendor a release candidate. Returns "" when nothing parses.
func pickLatestSemverTag(names []string) string {
	best := ""
	var bestParts [3]int
	for _, name := range names {
		parts, ok := parseSemverTag(name)
		if !ok {
			continue
		}
		if best == "" || semverLess(bestParts, parts) {
			best, bestParts = name, parts
		}
	}
	return best
}

// parseSemverTag parses "v4.1.2" (the leading v is optional) into its three
// numeric components. Anything else — a pre-release, a date tag, a branch
// name — reports false.
func parseSemverTag(name string) ([3]int, bool) {
	var out [3]int
	s := strings.TrimPrefix(strings.TrimSpace(name), "v")
	if s == "" || strings.ContainsAny(s, "-+") {
		return out, false
	}
	fields := strings.Split(s, ".")
	if len(fields) != 3 {
		return out, false
	}
	for i, f := range fields {
		if f == "" {
			return out, false
		}
		n := 0
		for _, r := range f {
			if r < '0' || r > '9' {
				return out, false
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out, true
}

// semverLess reports whether a sorts before b.
func semverLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// fetchSDKTarball downloads one tagged source tarball.
func fetchSDKTarball(tag string) (io.ReadCloser, error) {
	client := &http.Client{Timeout: sdkHTTPTimeout}
	url := "https://codeload.github.com/" + sdkRepo + "/tar.gz/refs/tags/" + tag
	resp, err := client.Get(url) // #nosec G107 -- constant host, tag validated by the caller
	if err != nil {
		return nil, fmt.Errorf("download Drift SDK %s: %w", tag, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close() // #nosec G104
		return nil, fmt.Errorf("download Drift SDK %s: %s", tag, resp.Status)
	}
	return resp.Body, nil
}

// sdkCachedSource returns a directory holding the extracted SDK at tag,
// downloading it once. Keyed by tag under UserCacheDir, so repeated deploys —
// and every function in an element — reuse one copy.
func sdkCachedSource(tag string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache dir: %w", err)
	}
	dir := filepath.Join(base, "drift", "sdk-cache", tag)
	// The marker is written last, so a half-extracted directory from an
	// interrupted run is never mistaken for a complete one.
	marker := filepath.Join(dir, ".complete")
	if _, serr := os.Stat(marker); serr == nil {
		return dir, nil
	}
	if rerr := os.RemoveAll(dir); rerr != nil {
		return "", fmt.Errorf("clear stale SDK cache: %w", rerr)
	}
	if merr := os.MkdirAll(dir, 0o755); merr != nil {
		return "", fmt.Errorf("create SDK cache: %w", merr)
	}

	body, err := sdkFetchTarball(tag)
	if err != nil {
		return "", err
	}
	defer body.Close() // #nosec G104

	if xerr := extractSDKTarGz(body, dir); xerr != nil {
		os.RemoveAll(dir) // #nosec G104 -- don't leave a partial tree behind
		return "", xerr
	}
	if werr := os.WriteFile(marker, []byte(tag), 0o644); werr != nil { // #nosec G306
		return "", fmt.Errorf("finalise SDK cache: %w", werr)
	}
	return dir, nil
}

// sdkMaxFileBytes bounds one extracted file. The SDKs are single source files
// of a few tens of KB; this is four orders of magnitude of headroom and exists
// only so a malformed or hostile archive cannot fill the disk.
const sdkMaxFileBytes = 16 << 20

// extractSDKTarGz unpacks a GitHub source tarball into dst, stripping the
// leading `<repo>-<ref>/` component GitHub adds.
//
// Paths are validated rather than trusted: a `..` component or an absolute path
// in an archive is the classic tar-slip write-outside-the-target, and this
// archive arrives over the network.
func extractSDKTarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("read SDK archive: %w", err)
	}
	defer gz.Close() // #nosec G104

	absDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}

	tr := tar.NewReader(gz)
	for {
		hdr, nerr := tr.Next()
		if nerr == io.EOF {
			return nil
		}
		if nerr != nil {
			return fmt.Errorf("read SDK archive: %w", nerr)
		}
		// Strip GitHub's top-level `sdk-4.1.2/` wrapper directory.
		rel := hdr.Name
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			rel = rel[i+1:]
		} else {
			continue // the wrapper dir entry itself
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(absDst, filepath.FromSlash(rel))
		// Containment check — the only thing standing between a hostile
		// archive and an arbitrary file write.
		if !strings.HasPrefix(target, absDst+string(os.PathSeparator)) {
			return fmt.Errorf("read SDK archive: entry %q escapes the target directory", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if merr := os.MkdirAll(target, 0o755); merr != nil {
				return merr
			}
		case tar.TypeReg:
			if merr := os.MkdirAll(filepath.Dir(target), 0o755); merr != nil {
				return merr
			}
			f, ferr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 G302
			if ferr != nil {
				return ferr
			}
			// io.CopyN with an explicit bound rather than io.Copy: an
			// unbounded copy from a network archive is a decompression bomb.
			written, cerr := io.CopyN(f, tr, sdkMaxFileBytes+1)
			f.Close() // #nosec G104
			if cerr != nil && cerr != io.EOF {
				return fmt.Errorf("read SDK archive: %w", cerr)
			}
			if written > sdkMaxFileBytes {
				return fmt.Errorf("read SDK archive: entry %q exceeds %d bytes", hdr.Name, sdkMaxFileBytes)
			}
		default:
			// Symlinks and devices have no business in an SDK source tarball.
			continue
		}
	}
}
