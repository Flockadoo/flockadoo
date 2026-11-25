//go:build !windows

package setup

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// E2 on the start before the dashboard: the engine is started with the chosen
// share, read right before the start, and the report carries it for the
// dashboard. A fake vllm writes the arguments it was started with.
func TestTheStartStartsVLLMWithTheChosenShareAndReportsIt(t *testing.T) {
	choice := ShareChoice{Share: 0.81, TotalBytes: peipeiTotal, FreeBytes: peipeiFree}
	useShare(t, choice)

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "vllm")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 0.34.0; exit 0; fi\necho \"$@\" > '" + argsFile + "'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	platform := httptest.NewServer(nil)
	platform.Close()
	cfg := autoStartConfig(t)
	cfg.Provider.EngineBin = bin
	cfg.Provider.URL = platform.URL

	var out bytes.Buffer
	report, _ := EnsureOnStartAt(t.Context(), cfg, "", Streams{In: strings.NewReader(""), Out: &out, Err: &out})

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the engine was not started: %v\n%s", err, out.String())
	}
	if !strings.Contains(string(got), "--gpu-memory-utilization 0.81") {
		t.Errorf("the engine was started with %q, want --gpu-memory-utilization 0.81", strings.TrimSpace(string(got)))
	}
	if report.MemoryShare != choice {
		t.Errorf("report share %+v, want %+v", report.MemoryShare, choice)
	}
	// The chosen share is not saved: the next start chooses again (D1).
	if strings.Contains(strings.Join(cfg.Provider.EngineArgs, " "), "gpu-memory-utilization") {
		t.Errorf("the share was saved into the engine command: %v", cfg.Provider.EngineArgs)
	}
}
