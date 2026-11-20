package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/logger"
)

// Plan item A2: the library default no longer writes files, so main installs
// the file logger itself. The subcommands and the start of the daemon still
// log to ~/.local/state/flockadoo/log, as before. (A new function: this test
// pins behaviour that already held and could not fail first.)
func TestTheEarlyLoggerWritesTheDefaultFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Cleanup(func() { logger.SetDefaultLogger(nil) })

	early := installEarlyLogger()
	if early == nil {
		t.Fatal("the early logger was not installed")
	}
	logger.Info("before the config is read")
	_ = early.Sync()
	if err := early.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".local", "state", "flockadoo", "log", "flockadoo.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "before the config is read") {
		t.Fatalf("flockadoo.log = %s", b)
	}
}
