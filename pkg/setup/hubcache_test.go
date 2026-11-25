package setup

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/testenv/fakeplatform"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Model revisions plan, "Where models go", item H1. provider.hf_hub_cache
// reached only the verifier, never hf or the engine, so a provider who set it
// downloaded to one folder and was measured in another. It is replaced by
// HF_HUB_CACHE, the variable hf, vLLM and the client all read. Client 0.46.0
// warned when it was set; from this release the value is ignored: a config
// that still has it loads, the client warns that it is ignored, and the
// verifier looks only where hf and the engine do.

func TestALeftoverHFHubCacheIsWarnedAboutAsIgnored(t *testing.T) {
	hub := filepath.Join(t.TempDir(), "hub")
	t.Setenv("HF_HUB_CACHE", hub)
	cfg := &config.Config{IgnoredHFHubCache: "/data/models"}
	resolved, warning, err := HubCacheNotice(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != hub {
		t.Errorf("resolved %q, want %q", resolved, hub)
	}
	for _, want := range []string{"hf_hub_cache", "/data/models", "ignored", hub, "HF_HUB_CACHE"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not say %q", warning, want)
		}
	}
	if strings.Contains(warning, "next release") {
		t.Errorf("warning %q still says the option goes in the next release", warning)
	}
}

func TestWithoutHFHubCacheThereIsNoWarning(t *testing.T) {
	hub := filepath.Join(t.TempDir(), "hub")
	t.Setenv("HF_HUB_CACHE", hub)
	resolved, warning, err := HubCacheNotice(&config.Config{})
	if err != nil || resolved != hub || warning != "" {
		t.Errorf("HubCacheNotice = %q, %q, %v; want %s and no warning", resolved, warning, err, hub)
	}
}

// Negative: a config that still sets provider.hf_hub_cache to another folder
// does not move the verifier. It measures the model in the Hugging Face cache
// hf downloaded to and the engine serves from.
func TestAnHFHubCacheInTheConfigDoesNotMoveTheVerifier(t *testing.T) {
	const sha = "aa8e72537993ba99e69dfaafa59ed015b17504d1"
	hub := t.TempDir()
	t.Setenv("HF_HUB_CACHE", hub)
	snap := filepath.Join(hub, verify.HFRepoToCacheDir("org/m"), "snapshots", sha)
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	header := `{"__metadata__":{},"w":{"dtype":"F32","shape":[1],"data_offsets":[0,4]}}`
	weights := append(binary.LittleEndian.AppendUint64(nil, uint64(len(header))), header...)
	weights = append(weights, 0, 0, 0, 0)
	for name, body := range map[string][]byte{"config.json": []byte(`{"model_type":"m"}`), "model.safetensors": weights} {
		if err := os.WriteFile(filepath.Join(snap, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The config as a provider still has it: hf_hub_cache names an empty folder.
	elsewhere := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("provider:\n  engine: vllm\n  model: org/m\n  hf_hub_cache: "+elsewhere+"\n  hf_revision: "+sha+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	plat := fakeplatform.New(t, "k")
	plat.Set(http.MethodGet, "/api/models/approved-builds", fakeplatform.JSON(http.StatusOK, map[string]any{"object": "list", "data": []map[string]any{
		{"id": "e1", "alias": "org/m", "service_type": "vllm", "hf_repo": "org/m", "hf_ref": "main", "is_active": true},
	}}))
	plat.Set(http.MethodPost, "/api/provider/verify-model", fakeplatform.JSON(http.StatusOK, map[string]string{"verification_status": "verified"}))
	catalog := verify.NewCatalog(plat.URL, "vllm")
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := NewModelVerifier(cfg, catalog, verify.NewServerClient(plat.URL, "k"))
	v.SetRevision(sha)
	v.Refresh(context.Background(), "org/m")

	reqs := plat.Requests(http.MethodPost, "/api/provider/verify-model")
	if len(reqs) != 1 {
		t.Fatalf("%d verify-model requests, want 1: the model in HF_HUB_CACHE was not measured", len(reqs))
	}
	var body struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Files) == 0 {
		t.Errorf("no files measured; want the snapshot in %s", hub)
	}
}
