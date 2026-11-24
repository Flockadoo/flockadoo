package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/verify"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
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

// GET /api/provider/network/engine and /network/node/engine as the server
// sends them since model revisions F1 (flockadoo-server pkg/api/provider
// EngineResponse, golden flock-engine.response.schema.json). hf_revision is
// the commit every machine in the flock serves. Decoding into the client's
// type and encoding again keeps every field the server sends.
func TestTheFlockCommandIsReadWhole(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "flock-engine.response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var remote winglink.Remote
	if err := json.Unmarshal(raw, &remote); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(again, raw) {
		t.Errorf("the flock command after the client read it:\n%s\nwant:\n%s", again, raw)
	}
}
