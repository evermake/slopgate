package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRepoID_Roundtrip(t *testing.T) {
	repoRoot := t.TempDir()

	if err := WriteRepoID(repoRoot, "01JB2XABCDEF"); err != nil {
		t.Fatalf("WriteRepoID() error = %v", err)
	}

	got, err := ReadRepoID(repoRoot)
	if err != nil {
		t.Fatalf("ReadRepoID() error = %v", err)
	}
	if got != "01JB2XABCDEF" {
		t.Fatalf("ReadRepoID() = %q, want %q", got, "01JB2XABCDEF")
	}

	// Confirm it landed at the expected path.
	path := filepath.Join(repoRoot, Dir, "repo-id")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected repo-id at %s: %v", path, err)
	}
}

func TestReadRepoID_Missing(t *testing.T) {
	repoRoot := t.TempDir()

	_, err := ReadRepoID(repoRoot)
	if err == nil {
		t.Fatal("ReadRepoID() error = nil, want error for missing repo-id")
	}
}

func TestReadRepoID_TrimsWhitespace(t *testing.T) {
	repoRoot := t.TempDir()
	dir := filepath.Join(repoRoot, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo-id"), []byte("  01JB2X  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRepoID(repoRoot)
	if err != nil {
		t.Fatalf("ReadRepoID() error = %v", err)
	}
	if got != "01JB2X" {
		t.Fatalf("ReadRepoID() = %q, want %q", got, "01JB2X")
	}
}
