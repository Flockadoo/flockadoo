package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeplatform"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
)

// Plan item Q2, decision DC11: with provider_type gone from the config file,
// the health report still sends the same provider_type for each engine,
// derived from engine. Passes before and after the change.
func TestTheHealthReportSendsTheEnginesPlatformType(t *testing.T) {
	cases := []struct{ engine, want string }{
		{"vllm", "vllm"},
		{"vllm-metal", "vllm"},
		{"llama_cpp", "llama_cpp"},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			eng := fakeengine.VLLM(t)
			plat := fakeplatform.New(t, "k")
			path := filepath.Join(t.TempDir(), "config.yaml")
			yaml := fmt.Sprintf("provider:\n  engine: %s\n  url: %s\n  api_key: k\n  llm_url: %s\n", tc.engine, plat.URL, eng.URL)
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			r := NewReporter(cfg, nil, llm.NewClient(tc.engine, eng.URL, 5*time.Second))
			if err := r.SendHealthReport(context.Background()); err != nil {
				t.Fatal(err)
			}
			sent := plat.AwaitRequests(t, http.MethodPost, "/api/provider/health", 1)
			var body struct {
				ProviderType string `json:"provider_type"`
			}
			if err := json.Unmarshal(sent[0].Body, &body); err != nil {
				t.Fatal(err)
			}
			if body.ProviderType != tc.want {
				t.Fatalf("engine %s sent provider_type %q, want %q", tc.engine, body.ProviderType, tc.want)
			}
		})
	}
}
