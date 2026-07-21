package atomic_common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ondrift/cli/v2/common"
)

// DetectLanguage scans dir for the file containing the @atomic annotation and
// returns the language ("native", "python", "node", "ruby", "php", "rust") and the filename (not full path).
func DetectLanguage(dir string) (string, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		ext := filepath.Ext(name)
		if _, err := ParseAtomicMetadata(path); err != nil {
			continue
		}
		switch ext {
		case ".py":
			return "python", name, nil
		case ".js":
			return "node", name, nil
		case ".go":
			return "native", name, nil
		case ".rb":
			return "ruby", name, nil
		case ".php":
			return "php", name, nil
		case ".rs":
			return "rust", name, nil
		}
	}
	return "", "", fmt.Errorf("no atomic function found in %s", dir)
}

// FuncNameForLanguage returns the expected handler function name given the
// @atomic method+name and the target language. Route patterns with path
// parameters (e.g., "users/:id") are normalized first: colons are stripped
// and slashes become hyphens, so "users/:id" → "users-id" before casing.
func FuncNameForLanguage(method, name, language string) string {
	// Normalize parameterized route patterns into plain kebab-case.
	name = strings.ReplaceAll(name, ":", "")
	name = strings.ReplaceAll(name, "/", "-")

	switch language {
	case "python", "ruby", "php", "rust":
		return toSnakeCase(method) + "_" + toSnakeCase(name)
	case "node":
		return toCamelCase(method + "-" + name)
	default:
		// Go: PascalCase
		namePascal := ""
		for _, seg := range strings.Split(name, "-") {
			namePascal += common.CapitalizeFirst(strings.ToLower(seg))
		}
		return common.CapitalizeFirst(strings.ToLower(method)) + namePascal
	}
}

// toSnakeCase converts "checkout-items" to "checkout_items" for Python function names.
func toSnakeCase(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// toCamelCase converts "checkout-items" to "checkoutItems" for Node function names.
func toCamelCase(name string) string {
	parts := strings.Split(name, "-")
	for i := 1; i < len(parts); i++ {
		parts[i] = common.CapitalizeFirst(strings.ToLower(parts[i]))
	}
	return strings.Join(parts, "")
}
