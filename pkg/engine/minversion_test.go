package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
)

// vLLM 0.30.0 is the oldest release Inferoute supports: the YaRN override is a
// rope_parameters dict, which needs the transformers 5 handling every release
// from 0.30 has. vLLM Metal reports the vLLM release it runs. llama.cpp builds
// are never too old, and a version that cannot be read is not treated as old.
func TestTooOld(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind    Kind
		version string
		old     bool
	}{
		{KindVLLM, "0.29.9", true},
		{KindVLLM, "0.29.2", true},
		{KindVLLM, "0.11.0", true},
		{KindVLLMMetal, "0.29.0", true},
		{KindVLLM, "0.30.0", false},
		{KindVLLM, "0.34.0", false},
		{KindVLLM, "0.31.0.post1", false},
		{KindVLLM, "1.0.0", false},
		{KindVLLMMetal, "0.33.0", false},
		{KindVLLM, "", false},
		{KindVLLM, "dev", false},
		{KindLlamaCpp, "b12359", false},
		{KindLlamaCpp, "0.1.0", false},
	}
	for _, tc := range cases {
		if got := TooOld(tc.kind, tc.version); got != tc.old {
			t.Errorf("TooOld(%s, %q) = %v, want %v", tc.kind, tc.version, got, tc.old)
		}
	}
}

func TestCheckVersionNamesTheReleaseTheMinimumAndTheFix(t *testing.T) {
	t.Parallel()
	err := CheckVersion(KindVLLM, "0.29.2")
	if !errors.Is(err, ErrEngineTooOld) {
		t.Fatalf("CheckVersion = %v", err)
	}
	for _, want := range []string{"0.29.2", "0.30.0", "inferoute-client setup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not mention %s", err, want)
		}
	}
	if err := CheckVersion(KindVLLM, "0.30.0"); err != nil {
		t.Fatalf("0.30.0: %v", err)
	}
}

// oldVLLM is a vllm that reports 0.29.2 and, if anyone runs `serve`, leaves a
// marker file behind.
func oldVLLM(t *testing.T) (bin, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "served")
	bin = filepath.Join(dir, "vllm")
	body := "#!/bin/sh\nif [ \"$1\" = serve ]; then : > '" + marker + "'; exit 1; fi\necho vLLM version 0.29.2\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		body = "@if \"%1\"==\"serve\" (type nul > \"" + marker + "\" & exit /b 1)\r\n@echo vLLM version 0.29.2\r\n"
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, marker
}

// Auto-start never starts a vLLM older than the minimum.
func TestStartAndWaitRefusesAnOldVLLM(t *testing.T) {
	bin, marker := oldVLLM(t)
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "Qwen/Qwen3-0.6B"
	cfg.Provider.EngineBin = bin
	cfg.Provider.LLMURL = "http://127.0.0.1:1" // nothing listens on port 1
	err := StartAndWait(context.Background(), cfg, t.TempDir())
	if !errors.Is(err, ErrEngineTooOld) {
		t.Fatalf("StartAndWait = %v, want ErrEngineTooOld", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the old vLLM was started")
	}
}
