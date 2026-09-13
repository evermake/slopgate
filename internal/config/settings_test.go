package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSettings(t *testing.T) {
	got := DefaultSettings()
	if got.MaxConcurrentAgents != 4 {
		t.Fatalf("DefaultSettings().MaxConcurrentAgents = %d, want 4", got.MaxConcurrentAgents)
	}
}

func TestLoadSettings_MissingFile(t *testing.T) {
	dir := t.TempDir()

	got, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v, want nil", err)
	}
	if got != DefaultSettings() {
		t.Fatalf("LoadSettings() = %+v, want defaults %+v", got, DefaultSettings())
	}
}

func TestLoadSettings_MissingSlopgateDir(t *testing.T) {
	// The whole .slopgate directory doesn't exist yet, not just the file.
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	got, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v, want nil", err)
	}
	if got != DefaultSettings() {
		t.Fatalf("LoadSettings() = %+v, want defaults %+v", got, DefaultSettings())
	}
}

func TestLoadSettings_KeyAbsent(t *testing.T) {
	dir := writeFile(t, "settings.json", `{}`)

	got, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v, want nil", err)
	}
	if got.MaxConcurrentAgents != 4 {
		t.Fatalf("MaxConcurrentAgents = %d, want default 4", got.MaxConcurrentAgents)
	}
}

func TestLoadSettings_KeyZero(t *testing.T) {
	dir := writeFile(t, "settings.json", `{"maxConcurrentAgents": 0}`)

	got, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v, want nil", err)
	}
	if got.MaxConcurrentAgents != 4 {
		t.Fatalf("MaxConcurrentAgents = %d, want default 4", got.MaxConcurrentAgents)
	}
}

func TestLoadSettings_KeyPositive(t *testing.T) {
	dir := writeFile(t, "settings.json", `{"maxConcurrentAgents": 8}`)

	got, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v, want nil", err)
	}
	if got.MaxConcurrentAgents != 8 {
		t.Fatalf("MaxConcurrentAgents = %d, want 8", got.MaxConcurrentAgents)
	}
}

func TestLoadSettings_KeyNegative(t *testing.T) {
	dir := writeFile(t, "settings.json", `{"maxConcurrentAgents": -1}`)

	_, err := LoadSettings(dir)
	if err == nil {
		t.Fatal("LoadSettings() error = nil, want error for negative maxConcurrentAgents")
	}
}

func TestLoadSettings_MalformedJSON(t *testing.T) {
	dir := writeFile(t, "settings.json", `{not json`)

	_, err := LoadSettings(dir)
	if err == nil {
		t.Fatal("LoadSettings() error = nil, want error for malformed JSON")
	}
}

// writeFile creates a fresh temp dir, writes name with contents inside it,
// and returns the dir path.
func writeFile(t *testing.T, name, contents string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return dir
}
