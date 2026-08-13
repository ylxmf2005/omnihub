package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAndEnsure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))

	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(paths.Database) != "omnihub.db" {
		t.Fatalf("database = %q", paths.Database)
	}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{paths.ConfigDir, paths.StateDir, paths.CacheDir, paths.RuntimeDir} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %s: %v", directory, err)
		}
	}
}
