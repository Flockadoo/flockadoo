package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/health"
)

// The unified memory plan (flockadoo-plans, item C1). A GB10 or a Mac reports
// its memory as unified: health.unified.request.json is that report, copied
// into flockadoo-server's tests/contract/testdata/client/<version>/ on the
// next client release, where the server decodes it (memory_kind, server
// S1). The client's own report type must carry every field of it, the kind
// included; the existing health.request.json (a dedicated GPU, no kind) is
// unchanged.
func TestAUnifiedMemoryReportKeepsItsMemoryKind(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "health.unified.request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report health.HealthReport
	if err := json.Unmarshal(want, &report); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got, want) {
		t.Fatalf("the client's report drops or changes a field of health.unified.request.json\ngot  %s\nwant %s", got, want)
	}
}
