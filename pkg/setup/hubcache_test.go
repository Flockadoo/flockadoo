package setup

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
)

// Model revisions plan, "Where models go". provider.hf_hub_cache reached only
// the verifier, never hf or the engine, so a provider who set it downloaded to
// one folder and was measured in another. It is replaced by HF_HUB_CACHE, the
// variable hf, vLLM and the client all read. This release warns when it is
// set, naming the folder actually used; the next one removes it. The client
// also says once at start which folder that is.

func TestHFHubCacheInTheConfigIsWarnedAbout(t *testing.T) {
	hub := filepath.Join(t.TempDir(), "hub")
	t.Setenv("HF_HUB_CACHE", hub)
	cfg := &config.Config{}
	cfg.Provider.HFHubCache = "/data/models"
	resolved, warning, err := HubCacheNotice(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != hub {
		t.Errorf("resolved %q, want %q", resolved, hub)
	}
	for _, want := range []string{"hf_hub_cache", "/data/models", hub, "HF_HUB_CACHE"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not say %q", warning, want)
		}
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
