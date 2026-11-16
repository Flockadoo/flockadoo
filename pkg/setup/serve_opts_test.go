package setup

import (
	"testing"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/verify"
)

// Setup saves the catalog's reasoning parser so auto-start passes it to vLLM.
func TestApplyServeOptsSavesTheReasoningParser(t *testing.T) {
	parser := "qwen3"
	cfg := &config.Config{}
	applyServeOpts(cfg, verify.CatalogEntry{Alias: "qwen/qwen3.6-35b-a3b", ReasoningParser: &parser}, "")
	if cfg.Provider.ReasoningParser != "qwen3" {
		t.Fatalf("saved reasoning parser = %q", cfg.Provider.ReasoningParser)
	}
	applyServeOpts(cfg, verify.CatalogEntry{Alias: "Qwen/Qwen2.5-7B-Instruct"}, "")
	if cfg.Provider.ReasoningParser != "" {
		t.Fatalf("switching to a model that does not reason kept %q", cfg.Provider.ReasoningParser)
	}
}
