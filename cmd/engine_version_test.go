package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
)

// At startup the client tells the operator when the engine already running is
// a vLLM below the minimum: it will not serve through it.
func TestStartupWarnsAboutAnOldRunningEngine(t *testing.T) {
	cases := []struct {
		version string
		warn    bool
	}{
		{"0.29.2", true},
		{"0.30.0", false},
		{"0.34.0", false},
	}
	for _, tc := range cases {
		eng := fakeengine.VLLM(t, fakeengine.Model("m"), fakeengine.Version(tc.version))
		cfg := &config.Config{}
		cfg.Provider.Model = "m"
		cfg.Provider.Engine = "vllm"
		client := llm.NewClient("vllm", eng.URL, 5*time.Second)
		msg := engineVersionWarning(context.Background(), cfg, client)
		if tc.warn != (msg != "") {
			t.Fatalf("vLLM %s: warning = %q, want warning %v", tc.version, msg, tc.warn)
		}
		if tc.warn && (!strings.Contains(msg, "0.30.0 or newer") || !strings.Contains(msg, "inferoute-client setup")) {
			t.Fatalf("vLLM %s: warning %q does not name the minimum and the fix", tc.version, msg)
		}
	}
}
