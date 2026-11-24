//go:build !windows

package setup

import (
	"os"
	"strings"
	"testing"
)

// list never opens a weight file: a GGUF is hashed in full, so measuring every
// cached model could take minutes. The weight file here cannot be read, so a
// list that opened it would fail. Windows has no unreadable mode bits to use.
func TestModelListNeverOpensAWeightFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode bits") //nolint:forbidigo // the test needs a file this user cannot read, which root always can
	}
	w := newModelWorld(t)
	p := w.weights(t, "org/m", curSHA, 2_000_000)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if err := w.run(t, "list"); err != nil {
		t.Fatal(err)
	}
	if l := line(w.out.String(), curSHA); !strings.Contains(l, "2.0 MB") {
		t.Errorf("line %q, want its size, 2.0 MB, read without opening it", l)
	}
}
