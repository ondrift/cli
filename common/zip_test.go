package common

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestZipFolder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('hello')"), 0o644)
	os.WriteFile(filepath.Join(dir, "drift.py"), []byte("# sdk"), 0o644)

	buf, err := ZipFolder(dir)
	if err != nil {
		t.Fatalf("ZipFolder: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty zip")
	}

	// Verify contents.
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	names := make(map[string]bool)
	for _, f := range r.File {
		names[f.Name] = true
	}
	if !names["app.py"] {
		t.Fatal("zip should contain app.py")
	}
	if !names["drift.py"] {
		t.Fatal("zip should contain drift.py")
	}
}

func TestZipFolderWithSubdirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src")
	os.MkdirAll(sub, 0o750)
	os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main"), 0o644)

	buf, err := ZipFolder(dir)
	if err != nil {
		t.Fatalf("ZipFolder: %v", err)
	}

	r, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	found := false
	for _, f := range r.File {
		if f.Name == "src/main.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("zip should contain src/main.go")
	}
}

func TestZipFolderEmpty(t *testing.T) {
	dir := t.TempDir()
	buf, err := ZipFolder(dir)
	if err != nil {
		t.Fatalf("ZipFolder empty: %v", err)
	}
	// Should still be a valid (empty) zip.
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open empty zip: %v", err)
	}
	if len(r.File) != 0 {
		t.Fatalf("expected 0 files, got %d", len(r.File))
	}
}

func TestZipFolderNonexistent(t *testing.T) {
	_, err := ZipFolder("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent folder")
	}
}

func TestZipFolderPreservesContent(t *testing.T) {
	dir := t.TempDir()
	content := "hello world 🚀"
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0o644)

	buf, _ := ZipFolder(dir)
	r, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))

	for _, f := range r.File {
		if f.Name == "test.txt" {
			rc, _ := f.Open()
			data := make([]byte, 100)
			n, _ := rc.Read(data)
			rc.Close()
			if string(data[:n]) != content {
				t.Fatalf("content: got %q, want %q", data[:n], content)
			}
			return
		}
	}
	t.Fatal("test.txt not found in zip")
}
