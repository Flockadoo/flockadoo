package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/testenv/fakeengine"
	"github.com/Flockadoo/flockadoo/pkg/llm"
)

// Plan item Q4, finding ENG-4: the deployment fingerprint takes the engine's
// version from the running engine, so an upgrade invalidates the benchmark
// and the capability results (invariant I9), and computing it starts no
// process.

func TestTheFingerprintFollowsTheRunningEnginesVersion(t *testing.T) {
	cases := []struct {
		engine        string
		start         func(t testing.TB, opts ...fakeengine.Option) *fakeengine.Engine
		before, after string
	}{
		{"vllm", fakeengine.VLLM, "0.28.0", "0.33.0"},
		{"llama_cpp", fakeengine.LlamaCpp, "b12359", "b12400"},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			eng := tc.start(t, fakeengine.Model("m"), fakeengine.Version(tc.before))
			cfg := &config.Config{}
			cfg.Provider.Model = "m"
			cfg.Provider.Engine = tc.engine // the engine is on PATH: no engine_bin
			client := llm.NewClient(tc.engine, eng.URL, 5*time.Second)

			first := deploymentFingerprint(context.Background(), cfg, client, nil)
			if first == "" || first != deploymentFingerprint(context.Background(), cfg, client, nil) {
				t.Fatalf("fingerprint is empty or not stable: %q", first)
			}
			eng.SetVersion(tc.after)
			if again := deploymentFingerprint(context.Background(), cfg, client, nil); again == first {
				t.Fatalf("the engine went from %s to %s and the fingerprint stayed %q", tc.before, tc.after, first)
			}
		})
	}
}

func TestTheFingerprintStartsNoProcess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	bin := filepath.Join(dir, "vllm")
	body := "#!/bin/sh\ntouch '" + marker + "'\necho vLLM version 0.31.0\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		body = "@type nul > \"" + marker + "\"\r\n@echo vLLM version 0.31.0\r\n"
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	eng := fakeengine.VLLM(t, fakeengine.Model("m"))
	cfg := &config.Config{}
	cfg.Provider.Model = "m"
	cfg.Provider.Engine = "vllm"
	cfg.Provider.EngineBin = bin
	if fp := deploymentFingerprint(context.Background(), cfg, llm.NewClient("vllm", eng.URL, 5*time.Second), nil); fp == "" {
		t.Fatal("no fingerprint")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("computing the fingerprint ran the engine binary")
	}
}
