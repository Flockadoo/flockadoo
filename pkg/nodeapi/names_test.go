package nodeapi

import "testing"

// The version headers are X-Flockadoo-… (docs/naming.md in internal-docs);
// the server reads nothing under the old names.
func TestVersionHeaderNames(t *testing.T) {
	if HeaderClientVersion != "X-Flockadoo-Client-Version" || HeaderLatestClientVersion != "X-Flockadoo-Latest-Client-Version" {
		t.Errorf("headers %q, %q", HeaderClientVersion, HeaderLatestClientVersion)
	}
}
