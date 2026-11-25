//go:build !windows

package engine

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
)

// E3 end to end: a fake vllm that refuses to start the way vLLM did on peipei.
// StartAndWait's error is the refusal in plain words, not "engine exited".

// fakeVLLM writes a vllm that answers --version and, when started, prints
// line and exits 1.
func fakeVLLM(t *testing.T, line string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "vllm")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 0.34.0; exit 0; fi\ncat <<'EOF'\n" + line + "\nEOF\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func refusingConfig(t *testing.T, line string) (*config.Config, string) {
	t.Helper()
	down := httptest.NewServer(nil)
	down.Close()
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "baai/bge-m3"
	cfg.Provider.EngineBin = fakeVLLM(t, line)
	cfg.Provider.LLMURL = down.URL
	return cfg, t.TempDir()
}

func TestAStartRefusedForMemorySaysSo(t *testing.T) {
	cfg, logDir := refusingConfig(t, peipeiRefusal)
	err := StartAndWait(t.Context(), cfg, logDir)
	if !errors.Is(err, ErrNotEnoughMemory) {
		t.Fatalf("StartAndWait = %v, want the memory refusal", err)
	}
	for _, want := range []string{"112.11 GiB", "105.25 GiB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}

// Negative: an earlier start's refusal in the log does not explain this one.
func TestAnEarlierRefusalInTheLogIsNotBlamed(t *testing.T) {
	cfg, logDir := refusingConfig(t, "the engine exited for another reason")
	if err := os.WriteFile(LogPath(logDir), []byte(peipeiRefusal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := StartAndWait(t.Context(), cfg, logDir)
	if err == nil || errors.Is(err, ErrNotEnoughMemory) {
		t.Errorf("StartAndWait = %v, want a failure that is not about memory", err)
	}
}
