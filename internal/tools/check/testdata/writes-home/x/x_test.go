package x

import (
	"os"
	"path/filepath"
	"testing"
)

// A test that leaves a file in the home directory, as the client's tests did
// through the default logger (finding TST-1).
func TestWritesIntoHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".local", "state", "app")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}
