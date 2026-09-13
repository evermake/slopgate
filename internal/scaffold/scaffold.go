// Package scaffold writes the files slopgate installs into a repository and
// into its bare gate repo: the two git hooks, and the default .slopgate/ tree.
//
// Everything here runs at `slopgate init` and never again. Per core constraint
// 1, slopgate writes nothing into the repo after init.
package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed files
var files embed.FS

// HookParams are substituted into the hook templates at install time. Binary is
// an absolute path rather than a bare `slopgate`: a git hook runs with a
// stripped environment, and relying on PATH is how hooks mysteriously stop
// working.
type HookParams struct {
	Binary string
	RepoID string
}

// InstallHooks writes pre-receive and post-receive into the bare gate repo,
// overwriting any previous versions so that `init` stays idempotent and an
// upgraded binary re-points the hooks at itself.
func InstallHooks(gateRepoDir string, p HookParams) error {
	if p.Binary == "" || p.RepoID == "" {
		return fmt.Errorf("scaffold: hook params incomplete")
	}
	hooksDir := filepath.Join(gateRepoDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"pre-receive", "post-receive"} {
		raw, err := files.ReadFile("files/hooks/" + name + ".tmpl")
		if err != nil {
			return err
		}
		tmpl, err := template.New(name).Parse(string(raw))
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, p); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(hooksDir, name), buf.Bytes(), 0o755); err != nil {
			return fmt.Errorf("scaffold: write %s hook: %w", name, err)
		}
	}
	return nil
}

// EnsureRepoConfig creates .slopgate/ in repoRoot from the embedded defaults,
// skipping any file that already exists, and returns the paths it created.
//
// Never overwriting is deliberate: re-running init must not clobber rules or
// scripts the developer has written.
func EnsureRepoConfig(repoRoot string) ([]string, error) {
	var created []string
	root := "files/slopgate"
	err := fs.WalkDir(files, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		dest := filepath.Join(repoRoot, ".slopgate", rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
		data, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if filepath.Ext(dest) == ".sh" {
			mode = 0o755
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return err
		}
		created = append(created, dest)
		return nil
	})
	return created, err
}
