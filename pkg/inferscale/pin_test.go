package inferscale

import (
	"errors"
	"strings"
	"testing"
)

// Plan item S1, decision DC6: the Inferscale release and the SHA-256 of each
// of its files are fixed in the client's build (release.json, embedded); the
// client never takes a version or a checksum from anywhere at run time.

func TestTheVersionComesFromThePinOnly(t *testing.T) {
	t.Setenv("INFERSCALE_VERSION", "v9.9.9-from-the-environment")
	p := testPin()
	if got := downloadURLFor(p, "inferscale-linux-amd64"); !strings.Contains(got, "/download/"+p.Version+"/inferscale-linux-amd64") {
		t.Fatalf("download URL %q is not the pinned release %s", got, p.Version)
	}
	if strings.Contains(downloadURLFor(p, "inferscale-linux-amd64"), "from-the-environment") {
		t.Fatal("INFERSCALE_VERSION still changes the release")
	}
}

func TestEveryAssetHasItsPinnedHash(t *testing.T) {
	p := testPin()
	sum, err := p.sum("inferscale-linux-amd64")
	if err != nil || sum != strings.Repeat("a", 64) {
		t.Fatalf("pinned sum = %q, %v", sum, err)
	}
	if _, err := p.sum("inferscale-plan9-amd64"); !errors.Is(err, ErrNotPinned) {
		t.Fatalf("an asset with no pin gave %v, want ErrNotPinned", err)
	}
	if _, err := (Pin{}).sum("inferscale-linux-amd64"); !errors.Is(err, ErrNotPinned) {
		t.Fatalf("an empty pin gave %v, want ErrNotPinned", err)
	}
}

// The embedded pin file parses, and whatever it pins is well formed.
func TestTheEmbeddedPinParses(t *testing.T) {
	p, err := embeddedPin()
	if err != nil {
		t.Fatal(err)
	}
	for name, sum := range p.SHA256 {
		if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
			t.Errorf("%s: %q is not a SHA-256", name, sum)
		}
	}
}

func testPin() Pin {
	return Pin{Version: "v0.0.42", Commit: "abc123", SHA256: map[string]string{"inferscale-linux-amd64": strings.Repeat("a", 64)}}
}
