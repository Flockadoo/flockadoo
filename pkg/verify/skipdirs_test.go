package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Model revisions plan, item G1. The folders no Flockadoo engine loads are
// listed three times: getModels SKIP_MANIFEST_DIRS, the server judge's
// altWeightDirs, and here. This list also drives the downloader's --exclude
// (C1), so the client neither downloads nor measures them. If one copy
// changed alone, getModels, the client and the judge would disagree about
// which files make up a model. Each repo compares its list with one golden
// list, kept in flockadoo-server and copied here.
func TestTheClientsSkipListIsTheGoldenList(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "contract", "testdata", "skip-dirs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Dirs []string `json:"dirs"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if got := SkipManifestDirs(); !slices.Equal(got, golden.Dirs) {
		t.Errorf("SkipManifestDirs() %v, want the golden list %v", got, golden.Dirs)
	}
}
