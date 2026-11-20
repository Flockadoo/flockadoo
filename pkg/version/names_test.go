package version

import (
	"runtime"
	"testing"
)

// The client's User-Agent is flockadoo/<version> (<os>; <arch>)
// (docs/naming.md in internal-docs); the server reads the version and the OS
// from it.
func TestUserAgentName(t *testing.T) {
	want := "flockadoo/" + Version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
	if got := UserAgent(); got != want {
		t.Errorf("UserAgent() = %q, want %q", got, want)
	}
}
