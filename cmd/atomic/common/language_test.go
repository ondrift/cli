package atomic_common

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		filename string
		content  string
		wantLang string
	}{
		{"main.go", "// @atomic http=get:hello auth=none\npackage main", "native"},
		{"app.py", "# @atomic http=get:hello auth=none\ndef get_hello(req): pass", "python"},
		{"app.js", "// @atomic http=get:hello auth=none\nmodule.exports = (req) => {}", "node"},
		{"app.rb", "# @atomic http=get:hello auth=none\ndef get_hello(req); end", "ruby"},
		{"app.php", "// @atomic http=get:hello auth=none\n<?php", "php"},
		{"app.rs", "// @atomic http=get:hello auth=none\nfn main() {}", "rust"},
	}
	for _, tt := range tests {
		t.Run(tt.wantLang, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, tt.filename), []byte(tt.content), 0o644)

			lang, file, err := DetectLanguage(dir)
			if err != nil {
				t.Fatalf("DetectLanguage: %v", err)
			}
			if lang != tt.wantLang {
				t.Fatalf("lang: got %q, want %q", lang, tt.wantLang)
			}
			if file != tt.filename {
				t.Fatalf("file: got %q, want %q", file, tt.filename)
			}
		})
	}
}

func TestDetectLanguage_NoAnnotation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)

	_, _, err := DetectLanguage(dir)
	if err == nil {
		t.Fatal("expected error when no atomic annotation present")
	}
}

func TestDetectLanguage_EmptyDir(t *testing.T) {
	_, _, err := DetectLanguage(t.TempDir())
	if err == nil {
		t.Fatal("expected error for empty directory")
	}
}

func TestFuncNameForLanguage(t *testing.T) {
	tests := []struct {
		method, name, language, want string
	}{
		// Go: PascalCase
		{"get", "menu", "native", "GetMenu"},
		{"post", "checkout-items", "native", "PostCheckoutItems"},

		// Python: snake_case
		{"get", "menu", "python", "get_menu"},
		{"post", "checkout-items", "python", "post_checkout_items"},

		// Node: camelCase
		{"get", "menu", "node", "getMenu"},
		{"post", "checkout-items", "node", "postCheckoutItems"},

		// Ruby: snake_case
		{"get", "menu", "ruby", "get_menu"},

		// PHP: snake_case
		{"post", "order", "php", "post_order"},

		// Rust: snake_case
		{"get", "status", "rust", "get_status"},

		// Path parameters: strip colons, replace slashes with hyphens.
		{"get", "users/:id", "native", "GetUsersId"},
		{"get", "users/:id", "python", "get_users_id"},
		{"get", "users/:id", "node", "getUsersId"},
		{"get", "users/:id/posts/:postId", "native", "GetUsersIdPostsPostid"},
	}
	for _, tt := range tests {
		t.Run(tt.language+"_"+tt.method+"_"+tt.name, func(t *testing.T) {
			got := FuncNameForLanguage(tt.method, tt.name, tt.language)
			if got != tt.want {
				t.Errorf("FuncNameForLanguage(%q, %q, %q) = %q, want %q",
					tt.method, tt.name, tt.language, got, tt.want)
			}
		})
	}
}

func TestToSnakeCase(t *testing.T) {
	tests := []struct{ input, want string }{
		{"hello-world", "hello_world"},
		{"simple", "simple"},
		{"a-b-c", "a_b_c"},
		{"no-hyphen", "no_hyphen"},
	}
	for _, tt := range tests {
		if got := toSnakeCase(tt.input); got != tt.want {
			t.Errorf("toSnakeCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToCamelCase(t *testing.T) {
	tests := []struct{ input, want string }{
		{"get-menu", "getMenu"},
		{"post-checkout-items", "postCheckoutItems"},
		{"simple", "simple"},
	}
	for _, tt := range tests {
		if got := toCamelCase(tt.input); got != tt.want {
			t.Errorf("toCamelCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
