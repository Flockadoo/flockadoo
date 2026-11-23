package main

import (
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
)

// The console and dashboard hide an empty Benchmark line, which left a
// provider unsure whether the benchmark ran. When it does not run, the line
// says so and why; when it runs, there is no such line and it runs as before.
func TestBenchmarkSkipNote(t *testing.T) {
	limit := func(n int) *int { return &n }
	cases := []struct {
		name     string
		engine   string
		explicit *int
		want     string
	}{
		{"vLLM measures its concurrency", "vllm", nil, ""},
		{"vLLM Metal measures its concurrency", "vllm-metal", nil, ""},
		{"llama.cpp runs at its fixed limit", "llama_cpp", nil, "not run: llama.cpp uses a fixed limit of 1"},
		{"a limit in config is used as written", "vllm", limit(3), "not run: limit of 3 set in config"},
		{"a limit in config wins on llama.cpp too", "llama_cpp", limit(2), "not run: limit of 2 set in config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Provider.Engine = tc.engine
			cfg.Server.MaxConcurrentInference = tc.explicit
			if got := benchmarkSkipNote(cfg); got != tc.want {
				t.Fatalf("benchmarkSkipNote = %q, want %q", got, tc.want)
			}
		})
	}
}
