package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// GET /api/models/approved-builds as the server sends it since model
// revisions S3 (flockadoo-server pkg/modelverify CatalogRevision). The client
// reads each entry's revisions: the commit it downloads and serves. Decoding
// into the client's types and encoding again keeps every revision field; a
// field the client's type lacks would be dropped silently.
func TestTheCatalogsRevisionsAreRead(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "approved-builds.response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Data []verify.CatalogEntry `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	revisions := func(b []byte) []any {
		var body struct {
			Data []struct {
				Revisions []any `json:"revisions"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		var all []any
		for _, e := range body.Data {
			all = append(all, e.Revisions...)
		}
		return all
	}
	want, got := revisions(raw), revisions(again)
	if len(want) != 3 {
		t.Fatalf("the golden holds %d revisions, want 3", len(want))
	}
	if !jsonEqual(mustJSON(t, got), mustJSON(t, want)) {
		t.Errorf("revisions after the client read the catalog:\n%s\nwant:\n%s", mustJSON(t, got), mustJSON(t, want))
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
