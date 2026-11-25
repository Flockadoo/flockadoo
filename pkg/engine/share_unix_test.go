//go:build !windows

package engine

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
)

// E2 end to end: StartAndWait applies its options to the command it starts.
// A fake vllm writes the arguments it was started with and exits.
func TestStartAndWaitStartsTheAdjustedCommand(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "vllm")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 0.34.0; exit 0; fi\necho \"$@\" > '" + argsFile + "'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	down := httptest.NewServer(nil)
	down.Close()
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "baai/bge-m3"
	cfg.Provider.EngineBin = bin
	cfg.Provider.LLMURL = down.URL

	var sawKind Kind
	share := func(kind Kind, spec *Spec) {
		sawKind = kind
		*spec = spec.WithMemoryShare(0.81)
	}
	_ = StartAndWait(t.Context(), cfg, t.TempDir(), share)

	if sawKind != KindVLLM {
		t.Errorf("the option saw kind %q, want vllm", sawKind)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the engine was not started: %v", err)
	}
	if !strings.Contains(string(got), "--gpu-memory-utilization 0.81") {
		t.Errorf("the engine was started with %q, want --gpu-memory-utilization 0.81", strings.TrimSpace(string(got)))
	}
}
