// env.go — the Driftfile variable origin hierarchy.
//
// `$VAR` / `${VAR}` references in a Driftfile (and `$ENVREF` secret values)
// resolve from the process environment. We layer additional sources beneath the
// real environment so a deploy "just works" without a manual `source .env`,
// while never letting a convenience source override something set on purpose.
//
// Precedence, highest first:
//
//  1. Driftfile-hardcoded literals — a value written directly in the Driftfile.
//     Absolute; never looked up. (Handled by the parser: a non-`$` value is kept
//     verbatim.)
//  2. Terminal environment — variables already exported in the shell session.
//  3. `--secret KEY=value` / `--env <name>` override flags.
//  4. The `.env` file sitting next to the Driftfile.
//
// We realise tiers 2–4 by gap-filling the process environment in order: the real
// environment is left untouched, override flags fill only what it didn't set,
// and the `.env` file fills only what remains. The parser then reads
// `os.LookupEnv` and naturally sees the highest-precedence value.
package project

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const envFileName = ".env"

// varSummary records what each lower-precedence source contributed, for an
// honest, value-free report (names only — values may be secret, and secret keys
// are already visible in the Driftfile anyway).
type varSummary struct {
	fromOverrides []string              // KEY, applied from a flag
	fromEnvFile   []envFileContribution // KEY + which env file it came from
	shadowed      []string              // "KEY (.env)" / "KEY (override)" — a higher tier already had it
}

// envFileContribution ties a sourced variable to the file it came from, so the
// report can distinguish .env.<env> from the base .env.
type envFileContribution struct{ key, file string }

// applyVariableSources layers the override flags and the sibling env file(s)
// beneath the existing environment, per the precedence above. dir holds the
// Driftfile; overrides are raw "KEY=value" strings (from --secret and --env);
// env selects the active environment (sources `.env.<env>` ahead of the base
// `.env`, so the environment-specific file wins); useEnvFile=false skips env
// files entirely.
func applyVariableSources(dir string, overrides []string, useEnvFile bool, env string) (varSummary, error) {
	var s varSummary

	// Tier 3: override flags. Gap-fill — the real environment (tier 2) wins.
	for _, raw := range overrides {
		k, v, err := parseAssignment(raw)
		if err != nil {
			return s, err
		}
		if _, set := os.LookupEnv(k); set {
			s.shadowed = append(s.shadowed, k+" (override)")
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return s, fmt.Errorf("set %s: %w", k, err)
		}
		s.fromOverrides = append(s.fromOverrides, k)
	}

	// Tier 4: the sibling env file(s). Gap-fill beneath everything above. The
	// environment-specific `.env.<env>` is sourced FIRST so it out-ranks the
	// base `.env` (gap-fill = first writer wins).
	if useEnvFile {
		files := []string{}
		if env != "" {
			files = append(files, envFileName+"."+env)
		}
		files = append(files, envFileName)
		for _, fname := range files {
			vars, names, err := loadEnvFile(filepath.Join(dir, fname))
			if err != nil {
				return s, err
			}
			for _, k := range names { // file order, deterministic
				if _, set := os.LookupEnv(k); set {
					s.shadowed = append(s.shadowed, k+" ("+fname+")")
					continue
				}
				if err := os.Setenv(k, vars[k]); err != nil {
					return s, fmt.Errorf("set %s from %s: %w", k, fname, err)
				}
				s.fromEnvFile = append(s.fromEnvFile, envFileContribution{key: k, file: fname})
			}
		}
	}

	return s, nil
}

// report writes a concise summary to stderr so the layering is never silent.
func (s varSummary) report() {
	if len(s.fromEnvFile) > 0 {
		byFile := map[string][]string{}
		var order []string
		for _, c := range s.fromEnvFile {
			if _, seen := byFile[c.file]; !seen {
				order = append(order, c.file)
			}
			byFile[c.file] = append(byFile[c.file], c.key)
		}
		for _, f := range order {
			fmt.Fprintf(os.Stderr, "→ %s: loaded %d variable(s): %s\n", f, len(byFile[f]), strings.Join(byFile[f], ", "))
		}
	}
	if len(s.fromOverrides) > 0 {
		fmt.Fprintf(os.Stderr, "→ overrides: set %d variable(s): %s\n", len(s.fromOverrides), strings.Join(s.fromOverrides, ", "))
	}
	if len(s.shadowed) > 0 {
		fmt.Fprintf(os.Stderr, "→ already set in the environment (kept; lower-tier value ignored): %s\n", strings.Join(s.shadowed, ", "))
	}
}

// parseAssignment splits a "KEY=value" override; the value may itself contain '='.
func parseAssignment(s string) (string, string, error) {
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return "", "", fmt.Errorf("invalid override %q: expected KEY=value", s)
	}
	key := strings.TrimSpace(s[:i])
	if !isEnvName(key) {
		return "", "", fmt.Errorf("invalid variable name %q in %q", key, s)
	}
	return key, s[i+1:], nil
}

// loadEnvFile parses a .env file: `KEY=value` lines, `#` comments, blank lines,
// an optional `export ` prefix, and optional surrounding single/double quotes.
// A missing file is not an error (returns empty); a malformed line is. Returns
// the values plus the keys in file order.
func loadEnvFile(path string) (map[string]string, []string, error) {
	f, err := os.Open(path) // #nosec G304 -- the Driftfile's sibling .env, by design
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", envFileName, err)
	}
	defer f.Close() // #nosec G307

	vars := map[string]string{}
	var order []string
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		i := strings.IndexByte(raw, '=')
		if i <= 0 {
			return nil, nil, fmt.Errorf("%s:%d: not a KEY=value line: %q", envFileName, n, sc.Text())
		}
		key := strings.TrimSpace(raw[:i])
		if !isEnvName(key) {
			return nil, nil, fmt.Errorf("%s:%d: invalid variable name %q", envFileName, n, key)
		}
		if _, dup := vars[key]; !dup {
			order = append(order, key)
		}
		vars[key] = unquote(strings.TrimSpace(raw[i+1:]))
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", envFileName, err)
	}
	return vars, order, nil
}

// unquote strips one layer of matching single or double quotes.
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// isEnvName reports whether s is a valid POSIX environment variable name.
func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
