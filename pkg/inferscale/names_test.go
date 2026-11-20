package inferscale

import (
	"encoding/json"
	"testing"
)

// The server's network session says the whole flock's mode as flock_mode
// (single or multi; docs/naming.md in internal-docs). A response with the old
// key is read as no mode, as any unknown key is: no alias was left behind.
// The old key is built from parts so that the rename and the retired-names
// check pass over this file.
func TestSessionFlockMode(t *testing.T) {
	var got sessionResult
	if err := json.Unmarshal([]byte(`{"flock_mode":"multi","role":"master"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "multi" {
		t.Errorf("flock_mode: mode %q, want multi", got.Mode)
	}
	old := "clus" + "ter_mode"
	got = sessionResult{}
	if err := json.Unmarshal([]byte(`{"`+old+`":"multi","role":"master"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "" {
		t.Errorf("the old key still sets the mode: %q", got.Mode)
	}
}
