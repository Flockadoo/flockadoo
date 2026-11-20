package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/testenv/fakeengine"
	"github.com/Flockadoo/flockadoo/internal/testenv/fakegateway"
	"github.com/Flockadoo/flockadoo/pkg/connect"
	"github.com/Flockadoo/flockadoo/pkg/health"
	"github.com/Flockadoo/flockadoo/pkg/llm"
)

// Plan item B2, first pull request (findings NET-9 and SEC-4, decisions DC1
// and DC3): inference has one way in, the tunnel, and serving a request makes
// no call to the server.

// The local listener answers the dashboard and its APIs, and no inference.
func TestTheLocalListenerServesNoInference(t *testing.T) {
	r := newRig(t, 1)
	r.s.healthReporter = health.NewReporter(r.s.config, nil, r.s.llmClient)
	local := httptest.NewServer(r.s.localMux())
	t.Cleanup(local.Close)

	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings"} {
		req, _ := http.NewRequest(http.MethodPost, local.URL+path, strings.NewReader(streamBody))
		req.Header.Set("X-Request-ID", "hmac-ok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s on the local listener = %d, want 404", path, resp.StatusCode)
		}
	}
	if n := len(r.eng.Requests()); n != 0 {
		t.Errorf("the local listener sent %d requests to the engine", n)
	}
	if n := len(r.plat.AllRequests()); n != 0 {
		t.Errorf("the local listener made %d calls to the server", n)
	}
}

// The dashboard and the three /api routes still answer.
func TestTheDashboardAndItsAPIsStillAnswer(t *testing.T) {
	r := newRig(t, 1)
	r.s.healthReporter = health.NewReporter(r.s.config, nil, r.s.llmClient)
	local := httptest.NewServer(r.s.localMux())
	t.Cleanup(local.Close)
	for _, path := range []string{"/", "/api/status", "/api/health", "/api/busy"} {
		resp, err := http.Get(local.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
}

// Serving an inference request over the tunnel makes no call to the server.
func TestAnInferenceRequestMakesNoCallToTheNode(t *testing.T) {
	r := newRig(t, 1)
	if status, body := r.call(t, streamBody, nil); status != http.StatusOK {
		t.Fatalf("status %d (%s)", status, body)
	}
	if reqs := r.plat.AllRequests(); len(reqs) != 0 {
		t.Fatalf("serving one request made %d calls to the server, the first to %s", len(reqs), reqs[0].Path)
	}
}

// With the server unreachable, a request that arrives over the tunnel is
// still served: the tunnel, dialled by the client with its key, is what says
// a request comes from Flockadoo.
func TestARequestIsServedWhileTheNodeIsUnreachable(t *testing.T) {
	eng := fakeengine.VLLM(t, fakeengine.Model(rigModel))
	gw := fakegateway.New(t, rigKey)
	cfg := &config.Config{}
	cfg.Provider.URL = "http://127.0.0.1:1" // nothing listens here
	cfg.Provider.APIKey = rigKey
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = rigModel
	s := &Server{config: cfg, llmClient: llm.NewClient("vllm", eng.URL, 30*time.Second)}
	s.SetMaxInflight(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = connect.New(gw.URL, rigKey, s.inferenceMux(), nil).Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	gw.AwaitSession(t)

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(streamBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "hmac-ok")
	resp, err := gw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", resp.StatusCode, body)
	}
}
