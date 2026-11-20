package setup

import (
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/verify"
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

// Setup saves the catalog's rope_theta with the other RoPE settings, so
// auto-start can rebuild the complete YaRN override offline; a model without
// YaRN clears all three.
func TestApplyServeOptsSavesRopeTheta(t *testing.T) {
	yarn, base, maxLen, theta := "yarn", int64(32768), int64(131072), 1000000.0
	cfg := &config.Config{}
	applyServeOpts(cfg, verify.CatalogEntry{
		Alias: "Qwen/Qwen2.5-7B-Instruct", MaxModelLen: &maxLen,
		RopeType: &yarn, RopeBaseContextLen: &base, RopeTheta: &theta,
	}, "")
	if cfg.Provider.RopeType != "yarn" || cfg.Provider.RopeBaseContextLen != 32768 || cfg.Provider.RopeTheta != 1000000 {
		t.Fatalf("saved rope = %q %d %v", cfg.Provider.RopeType, cfg.Provider.RopeBaseContextLen, cfg.Provider.RopeTheta)
	}
	applyServeOpts(cfg, verify.CatalogEntry{Alias: "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B", MaxModelLen: &maxLen}, "")
	if cfg.Provider.RopeType != "" || cfg.Provider.RopeBaseContextLen != 0 || cfg.Provider.RopeTheta != 0 {
		t.Fatalf("switching to a model without YaRN kept %q %d %v", cfg.Provider.RopeType, cfg.Provider.RopeBaseContextLen, cfg.Provider.RopeTheta)
	}
}
