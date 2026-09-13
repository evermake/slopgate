package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/evermake/slopgate/internal/config"
)

func TestReuseOrMintRepoID_MissingMints(t *testing.T) {
	id, err := reuseOrMintRepoID(t.TempDir())
	if err != nil {
		t.Fatalf("missing repo-id: %v", err)
	}
	if id == "" {
		t.Fatal("minted repo id is empty")
	}
}

func TestReuseOrMintRepoID_ExistingKept(t *testing.T) {
	root := t.TempDir()
	const want = "01JB2XABCDEF"
	if err := config.WriteRepoID(root, want); err != nil {
		t.Fatal(err)
	}
	got, err := reuseOrMintRepoID(root)
	if err != nil {
		t.Fatalf("existing repo-id: %v", err)
	}
	if got != want {
		t.Fatalf("reuseOrMintRepoID() = %q, want %q", got, want)
	}
}

func TestReuseOrMintRepoID_EmptyFileErrors(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, config.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo-id"), []byte(" \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := reuseOrMintRepoID(root)
	if err == nil {
		t.Fatal("empty repo-id: want error, not a new id")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty repo-id must not be treated as missing")
	}
}

func TestReuseOrMintRepoID_UnreadableErrors(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, config.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory named repo-id makes ReadFile fail without os.ErrNotExist.
	if err := os.Mkdir(filepath.Join(dir, "repo-id"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := reuseOrMintRepoID(root)
	if err == nil {
		t.Fatal("unreadable repo-id: want error, not a new id")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("non-missing read failure must not mint a new id")
	}
}
