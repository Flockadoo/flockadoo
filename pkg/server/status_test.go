package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/relay"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/health"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
)

func TestHandleDashboard(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.handleDashboard(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/api/status") {
		t.Fatal("dashboard html should poll /api/status")
	}
	if strings.Contains(body, "Errors:") {
		t.Fatal("dashboard still renders an Errors section")
	}
}

func TestHandleStatusMasksAPIKey(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.config.Provider.APIKey = "sk-abcdefghijklmnop"
	s.config.Provider.Engine = "vllm"
	s.config.Provider.LLMURL = "http://127.0.0.1:8000"
	s.healthReporter = health.NewReporter(s.config, nil, &fakeLLM{})

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var snap StatusSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.SessionStatus != "online" {
		t.Fatalf("session = %q", snap.SessionStatus)
	}
	if snap.ProviderType != "vllm" {
		t.Fatalf("provider_type = %q", snap.ProviderType)
	}
	if strings.Contains(snap.ProviderAPIKey, "efgh") || snap.ProviderAPIKey == s.config.Provider.APIKey {
		t.Fatalf("api key not masked: %q", snap.ProviderAPIKey)
	}
	if snap.LastHealthUpdate != "Never" {
		t.Fatalf("last_health_update = %q", snap.LastHealthUpdate)
	}
}

func TestSessionStatusWhileBenchmarking(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetBenchmarking(true)
	s.SetEngineNote("benchmarking (1/5)")
	snap := s.snapshot(context.Background())
	if snap.SessionStatus != "benchmarking" {
		t.Fatalf("session = %q", snap.SessionStatus)
	}
	if snap.EngineNote != "benchmarking (1/5)" {
		t.Fatalf("note = %q", snap.EngineNote)
	}
	s.SetBenchmarking(false)
	if s.snapshot(context.Background()).SessionStatus != "online" {
		t.Fatal("session should return to online")
	}
}

func TestCapabilitiesStayOnline(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetCapabilities("testing - 2/5 checked (still serving requests)")
	snap := s.snapshot(context.Background())
	if snap.SessionStatus != "online" {
		t.Fatalf("session = %q", snap.SessionStatus)
	}
	if snap.CapabilityLine != "testing - 2/5 checked (still serving requests)" {
		t.Fatalf("line = %q", snap.CapabilityLine)
	}
}

func TestWorkerStatusUsesMasterURL(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.config.Provider.ClusterMode = "worker"
	s.config.Provider.NodeToken = "node-token"
	s.config.Provider.LLMURL = "http://198.18.0.2:8000"
	s.config.Provider.EngineArgs = []string{"--headless", "--master-addr", "198.18.0.1", "--port", "8000"}

	if got := s.snapshot(context.Background()).LLMURL; got != "http://198.18.0.1:8000" {
		t.Fatalf("llm_url = %q", got)
	}
}

func TestRecentRequestsAreInferenceOnly(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	rec := httptest.NewRecorder()
	s.handleBusy(rec, httptest.NewRequest(http.MethodGet, "/api/busy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("busy status = %d", rec.Code)
	}
	if len(s.snapshot(context.Background()).RecentRequests) != 0 {
		t.Fatalf("busy request logged: %+v", s.snapshot(context.Background()).RecentRequests)
	}

	s.logRequest(http.MethodPost, "/v1/chat/completions", http.StatusOK, relay.Completed, time.Now())
	st := httptest.NewRecorder()
	s.handleStatus(st, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if strings.Contains(st.Body.String(), `"errors"`) {
		t.Fatalf("status includes errors: %s", st.Body.String())
	}
	var snap StatusSnapshot
	if err := json.Unmarshal(st.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.RecentRequests) != 1 || snap.RecentRequests[0].Path != "/v1/chat/completions" {
		t.Fatalf("recent = %+v", snap.RecentRequests)
	}
}

// versionedEngine serves one model and reports its release, as a running
// vLLM or vLLM Metal does.
type versionedEngine struct {
	fakeLLM
	version string
}

func (e *versionedEngine) ListModels(context.Context) (*llm.ListModelsResponse, error) {
	return &llm.ListModelsResponse{Models: []llm.Model{{ID: "Qwen/Qwen2.5-3B-Instruct"}}}, nil
}

func (e *versionedEngine) EngineVersion(context.Context, bool) string { return e.version }

// engineServer is a server whose health reporter has just reported on a
// running vLLM Metal of the given release.
func engineServer(t *testing.T, eng *versionedEngine) *Server {
	t.Helper()
	s := newTestServer("http://unused", eng)
	s.config.Provider.Engine = "vllm-metal"
	s.healthReporter = health.NewReporter(s.config, nil, eng)
	if _, err := s.healthReporter.GetHealthReport(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// statusJSON is /api/status as the browser dashboard reads it.
func statusJSON(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// consoleOutput is what the terminal console draws.
func consoleOutput(t *testing.T, s *Server) string {
	t.Helper()
	var out bytes.Buffer
	s.console = &out
	if s.ctx == nil {
		s.ctx = context.Background()
	}
	s.redrawConsole()
	return out.String()
}

// An engine too old to serve is refused (the report carries no models and the
// node marks the cluster unhealthy). The console and the status API say so and
// why, instead of showing the session online and the model approved.
func TestTheStatusSaysWhyTheEngineIsNotServing(t *testing.T) {
	s := engineServer(t, &versionedEngine{version: "0.28.0"})

	reason, _ := statusJSON(t, s)["not_serving"].(string)
	for _, want := range []string{"vLLM Metal 0.28.0", "0.30.0 or newer", "inferoute-client setup"} {
		if !strings.Contains(reason, want) {
			t.Errorf("/api/status not_serving = %q, want it to contain %q", reason, want)
		}
	}

	console := consoleOutput(t, s)
	if !strings.Contains(console, "Not serving") || !strings.Contains(console, "vLLM Metal 0.28.0") {
		t.Errorf("the console does not say the engine is not serving:\n%s", console)
	}
}

// A supported engine shows no refusal, and one that is upgraded while the
// client runs clears the refusal it showed.
func TestASupportedEngineShowsNoRefusal(t *testing.T) {
	eng := &versionedEngine{version: "0.28.0"}
	s := engineServer(t, eng)
	eng.version = "0.33.0"
	if _, err := s.healthReporter.GetHealthReport(context.Background()); err != nil {
		t.Fatal(err)
	}

	if reason, ok := statusJSON(t, s)["not_serving"]; ok {
		t.Fatalf("/api/status not_serving = %q for a supported engine", reason)
	}
	if console := consoleOutput(t, s); strings.Contains(console, "Not serving") {
		t.Fatalf("the console says not serving for a supported engine:\n%s", console)
	}
}

// The browser dashboard draws the refusal from /api/status.
func TestTheDashboardShowsTheEngineRefusal(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer("http://unused", &fakeLLM{}).handleDashboard(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "s.not_serving") {
		t.Fatal("the dashboard does not render not_serving")
	}
}

func TestStopWithoutStartDoesNotPanic(t *testing.T) {
	s := &Server{}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on unstarted server: %v", err)
	}
}

func TestStartReturnsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = port
	cfg.Server.RequestTimeoutSeconds = 5
	s := &Server{config: cfg, ctx: context.Background()}
	if err := s.Start(); err == nil {
		t.Fatal("expected listen error when port is already taken")
	}
}
