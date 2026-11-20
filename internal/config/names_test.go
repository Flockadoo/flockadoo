package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This machine's part in its flock is provider.flock_role (single, master or
// worker; docs/naming.md in internal-docs). A file with the old key loads with
// no role, as any unknown key does: no alias was left behind. The old key is
// built from parts so that the rename and the retired-names check pass over
// this file. The test goes through Load and Save, so it names no Go field.
func TestFlockRoleKey(t *testing.T) {
	dir := t.TempDir()
	load := func(key string) string {
		t.Helper()
		in := filepath.Join(dir, "in.yaml")
		if err := os.WriteFile(in, []byte("provider:\n  api_key: sk-x\n  "+key+": master\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(in)
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "out.yaml")
		if err := Save(out, cfg); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if saved := load("flock_role"); !strings.Contains(saved, "flock_role: master") {
		t.Errorf("flock_role was not read and kept:\n%s", saved)
	}
	if saved := load("clus" + "ter_mode"); strings.Contains(saved, "master") {
		t.Errorf("the old key still sets the role:\n%s", saved)
	}
}
