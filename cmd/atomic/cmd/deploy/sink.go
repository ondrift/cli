package atomic_cmd

// sink.go — the swappable destination for a built Atomic function.
//
// The build paths (DeployFolder / DeployGoElement / DeployInterpretedElement)
// all funnel through sendSourceToOperator. That used to upload to the operator
// directly; now it routes through the active SlotSink. The default sink
// (operatorSink, in deploy.go) is the cloud upload, unchanged. `drift project
// run` swaps in localSlotSink to write the slice's on-disk slot layout to a
// local directory instead — reusing the EXACT same build, just a different
// destination. The slice then boot-scans that directory and serves the app with
// no control plane (atomic/ops/deploy_http.rs writes the same layout on the
// cloud path; this mirrors it minus the multi-tenant hardening).

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FuncArtifact is one built function ready to be placed: its metadata plus the
// build output (SourcePath = a native `app` binary, or an interpreted tar.gz of
// the slot contents) and the optional user-source archive.
type FuncArtifact struct {
	Name, Method, Language, Auth, Element, Stream string
	Secrets                                       []string
	Triggers                                      []TriggerSpec
	Digest                                        string
	SourcePath                                    string
	UserSourcePath                                string
}

// SlotSink consumes a built function. Default: upload to the operator. Local:
// write the on-disk slot layout for a standalone slice to boot-scan.
type SlotSink func(FuncArtifact) error

var slotSink SlotSink = operatorSink

// sendSourceToOperator is the build paths' single exit. It routes through the
// active sink so `drift project run` can redirect to local disk without
// touching any build logic. (Name kept for the call sites; it no longer always
// talks to the operator.)
func sendSourceToOperator(name, method, language, auth, element, stream string, secrets []string, sourcePath, userSourcePath string, triggers []TriggerSpec, digest string) error {
	return slotSink(FuncArtifact{
		Name: name, Method: method, Language: language, Auth: auth,
		Element: element, Stream: stream, Secrets: secrets,
		Triggers: triggers, Digest: digest,
		SourcePath: sourcePath, UserSourcePath: userSourcePath,
	})
}

// StageElementsLocally builds every function across `elements` and writes the
// slice slot layout under runnerDir, reusing the cloud build path with a local
// sink. The slice boot-scans runnerDir and serves them — no control plane.
func StageElementsLocally(elements []Element, runnerDir string, quiet bool) error {
	if err := os.MkdirAll(runnerDir, 0o755); err != nil {
		return err
	}
	prev := slotSink
	slotSink = localSlotSink(runnerDir)
	defer func() { slotSink = prev }()

	// Build in throwaway language containers so the user needs only Docker — no
	// go/cargo/pip/npm/composer/ruby on the host. Opt out with
	// DRIFT_RUN_HOST_BUILD=1 (faster if you already have the toolchains).
	if !hostBuildRequested() {
		base, err := containerBuildBaseDir()
		if err != nil {
			return err
		}
		// Staging tempdirs must live where the Docker VM shares the filesystem —
		// macOS does not share the default $TMPDIR (/var/folders/…), so a bind
		// mount of one would be empty. Point TMPDIR at the shared base for the
		// build and restore it after.
		prevTmp, hadTmp := os.LookupEnv("TMPDIR")
		os.Setenv("TMPDIR", base) // #nosec G104
		toolchainContainerMode = true
		defer func() {
			toolchainContainerMode = false
			if hadTmp {
				os.Setenv("TMPDIR", prevTmp) // #nosec G104
			} else {
				os.Unsetenv("TMPDIR") // #nosec G104
			}
		}()
	}

	for _, el := range elements {
		digest, _ := ElementDigest(el.Dir, el.Name)
		var err error
		switch {
		case el.Lang == "go":
			err = DeployGoElement(el, digest, quiet)
		case el.Lang == "python" || el.Lang == "node" || el.Lang == "ruby" || el.Lang == "php":
			err = DeployInterpretedElement(el, digest, quiet)
		case len(el.Funcs) == 1:
			err = DeployFolder(el.Dir, el.Name, quiet)
		default:
			err = fmt.Errorf("element %q is %s with %d functions — multi-function %s isn't staged for local run yet",
				el.Name, el.Lang, len(el.Funcs), el.Lang)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// localSlotSink writes one built function into runnerDir/<slot>/ exactly as the
// slice expects: the entry point (native binary → `app`; interpreted tar.gz →
// extracted app.<ext> + deps) plus metadata.json. The dir name is irrelevant to
// the boot-scan (it reads metadata.json), so a sanitised, unique name is fine.
func localSlotSink(runnerDir string) SlotSink {
	return func(a FuncArtifact) error {
		lang := a.Language
		if lang == "" {
			lang = "native"
		}
		slotDir := filepath.Join(runnerDir, slotDirName(a.Element, a.Method, a.Name))
		if err := os.RemoveAll(slotDir); err != nil {
			return err
		}
		if err := os.MkdirAll(slotDir, 0o755); err != nil {
			return err
		}

		if lang == "native" {
			if err := copyFileMode(a.SourcePath, filepath.Join(slotDir, "app"), 0o755); err != nil {
				return fmt.Errorf("place binary for %q: %w", a.Name, err)
			}
		} else if err := extractTarGz(a.SourcePath, slotDir); err != nil {
			return fmt.Errorf("extract source for %q: %w", a.Name, err)
		}

		// metadata.json — exactly the fields the slice boot-scan reads
		// (atomic/protocol.rs FunctionMetadata), written AFTER extraction so it
		// always wins. `secrets` MUST be an array, never null: the boot-scan
		// deserializes it into a Vec<String>, and a Go nil slice marshals to
		// `null`, which fails to parse and silently skips the function. (The
		// cloud deploy endpoint normalizes the same way.)
		secrets := a.Secrets
		if secrets == nil {
			secrets = []string{}
		}
		meta, _ := json.Marshal(map[string]any{
			"name": a.Name, "method": a.Method, "auth": a.Auth,
			"element": a.Element, "language": lang, "stream": a.Stream,
			"secrets": secrets,
		})
		return os.WriteFile(filepath.Join(slotDir, "metadata.json"), meta, 0o644)
	}
}

// slotDirName makes a filesystem-safe, unique slot directory name. The slice
// keys the registry off metadata.json, not the directory name.
func slotDirName(element, method, name string) string {
	var b strings.Builder
	for _, r := range element + "-" + method + "-" + name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "fn"
	}
	return s
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) // #nosec G304 — CLI reads its own build output
	if err != nil {
		return err
	}
	defer in.Close() // #nosec G307
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close() // #nosec G104
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// extractTarGz unpacks a .tar.gz into dst, path-traversal-safe. The source is
// the CLI's own build output (trusted), but the guard stays as defence in depth.
func extractTarGz(srcTarGz, dst string) error {
	f, err := os.Open(srcTarGz) // #nosec G304 — CLI reads its own build output
	if err != nil {
		return err
	}
	defer f.Close() // #nosec G307
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(dst, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode&0o777)) // #nosec G115
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { // #nosec G110 — trusted local build output, bounded by the build
				out.Close() // #nosec G104
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}
