package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/llm"
	"github.com/Flockadoo/flockadoo/pkg/logger"
	"github.com/Flockadoo/flockadoo/pkg/verify"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	logger.SetDefaultLogger(&logger.Logger{Logger: zap.NewNop()})
	os.Exit(m.Run())
}

type stubLLM struct {
	models *llm.ListModelsResponse
	err    error
}

func (s stubLLM) ListModels(context.Context) (*llm.ListModelsResponse, error) {
	return s.models, s.err
}

func (s stubLLM) ForwardRequest(context.Context, string, []byte, bool) (*http.Response, error) {
	return nil, errors.New("unused")
}

// versionedLLM is an engine that also answers the version lookup.
type versionedLLM struct {
	stubLLM
	version    string
	askedLlama *bool
}

func (v versionedLLM) EngineVersion(_ context.Context, llamaServer bool) string {
	if v.askedLlama != nil {
		*v.askedLlama = llamaServer
	}
	return v.version
}

func TestHealthReportCarriesEngineAndVersion(t *testing.T) {
	models := &llm.ListModelsResponse{Models: []llm.Model{{ID: "Qwen/Qwen2.5-7B-Instruct"}}}
	cases := []struct {
		name      string
		engine    string
		client    llm.Client
		wantKind  string
		wantVer   string
		wantLlama bool
	}{
		{"vllm", "vllm", versionedLLM{stubLLM: stubLLM{models: models}, version: "0.33.0"}, "vllm", "0.33.0", false},
		{"vllm metal", "vllm-metal", versionedLLM{stubLLM: stubLLM{models: models}, version: "0.28.0"}, "vllm-metal", "0.28.0", false},
		{"llama.cpp asks /props", "llama_cpp", versionedLLM{stubLLM: stubLLM{models: models}, version: "b11770"}, "llama_cpp", "b11770", true},
		// The lookup failed: the report still goes out, with the engine but no version.
		{"version lookup failed", "vllm", versionedLLM{stubLLM: stubLLM{models: models}}, "vllm", "", false},
		// A client without the lookup (a test double, a future engine client).
		{"client cannot tell", "vllm", stubLLM{models: models}, "vllm", "", false},
		// No engine configured (pointed at an existing server): say nothing about it.
		{"no engine configured", "", versionedLLM{stubLLM: stubLLM{models: models}, version: "0.33.0"}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var asked bool
			if v, ok := tc.client.(versionedLLM); ok {
				v.askedLlama = &asked
				tc.client = v
			}
			cfg := &config.Config{}
			cfg.Provider.Engine = tc.engine
			r := NewReporter(cfg, nil, tc.client)
			report, err := r.GetHealthReport(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.Engine != tc.wantKind || report.EngineVersion != tc.wantVer {
				t.Fatalf("engine = %q %q, want %q %q", report.Engine, report.EngineVersion, tc.wantKind, tc.wantVer)
			}
			if asked != tc.wantLlama {
				t.Fatalf("asked llama-server = %v, want %v", asked, tc.wantLlama)
			}
		})
	}
}

// newRefusalVerifier is a verifier whose node is never reached; only its
// engine refusal is under test.
func newRefusalVerifier() *verify.Verifier {
	return verify.NewVerifier(verify.NewCatalog("http://127.0.0.1:1", "vllm"), verify.NewServerClient("http://127.0.0.1:1", "k"), "vllm", "")
}

// A vLLM below the minimum serves no model through Flockadoo: the health
// report advertises none (the server deactivates what a push leaves out) and the
// verifier refuses every request with the reason. The version is still sent.
func TestAnOldEngineIsNotAdvertised(t *testing.T) {
	models := &llm.ListModelsResponse{Models: []llm.Model{{ID: "Qwen/Qwen2.5-7B-Instruct"}}}
	for _, engineName := range []string{"vllm", "vllm-metal"} {
		cfg := &config.Config{}
		cfg.Provider.Engine = engineName
		r := NewReporter(cfg, nil, versionedLLM{stubLLM: stubLLM{models: models}, version: "0.29.2"})
		v := newRefusalVerifier()
		r.SetVerifier(v)
		report, err := r.GetHealthReport(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Data) != 0 {
			t.Fatalf("%s 0.29.2: advertised %d models", engineName, len(report.Data))
		}
		if report.EngineVersion != "0.29.2" {
			t.Fatalf("%s: engine_version = %q", engineName, report.EngineVersion)
		}
		verdict, why := v.Verdict("Qwen/Qwen2.5-7B-Instruct")
		if verdict != verify.Refuse || !strings.Contains(why, "0.30.0 or newer") {
			t.Fatalf("%s: verdict = %v %q, want a refusal naming the minimum", engineName, verdict, why)
		}
	}
}

// A supported release, a llama.cpp build, and a version that cannot be read
// are all advertised, and an earlier refusal is lifted.
func TestASupportedEngineIsAdvertised(t *testing.T) {
	models := &llm.ListModelsResponse{Models: []llm.Model{{ID: "Qwen/Qwen2.5-7B-Instruct"}}}
	for _, tc := range []struct{ engine, version string }{
		{"vllm", "0.34.0"}, {"vllm", "0.30.0"}, {"vllm-metal", "0.33.0"}, {"llama_cpp", "b11770"}, {"vllm", ""},
	} {
		cfg := &config.Config{}
		cfg.Provider.Engine = tc.engine
		r := NewReporter(cfg, nil, versionedLLM{stubLLM: stubLLM{models: models}, version: tc.version})
		v := newRefusalVerifier()
		v.SetEngineRefusal("vLLM 0.29.2 is older than 0.30.0")
		r.SetVerifier(v)
		report, err := r.GetHealthReport(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Data) != 1 {
			t.Fatalf("%s %q: advertised %d models, want 1", tc.engine, tc.version, len(report.Data))
		}
		if _, why := v.Verdict("Qwen/Qwen2.5-7B-Instruct"); strings.Contains(why, "0.30.0") {
			t.Fatalf("%s %q: the refusal stayed: %q", tc.engine, tc.version, why)
		}
	}
}

func TestWorkerHealthReportShowsMasterModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/provider/network/node/health" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer node-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.Provider.URL = srv.URL
	cfg.Provider.FlockRole = "worker"
	cfg.Provider.NodeToken = "node-token"
	cfg.Provider.Engine = "vllm"

	r := NewReporter(cfg, nil, stubLLM{
		models: &llm.ListModelsResponse{Models: []llm.Model{{ID: "Qwen/Qwen2.5-7B-Instruct"}}},
	})
	if err := r.SendHealthReport(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.GetDisplayedModels()
	if len(got) != 1 || got[0].ID != "Qwen/Qwen2.5-7B-Instruct" {
		t.Fatalf("models = %+v", got)
	}

	r.llmClient = stubLLM{err: errors.New("master down")}
	if err := r.SendHealthReport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.GetDisplayedModels(); len(got) != 0 {
		t.Fatalf("models after master down = %+v", got)
	}
}
