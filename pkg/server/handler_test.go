package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/logger"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	logger.SetDefaultLogger(&logger.Logger{Logger: zap.NewNop()})
	os.Exit(m.Run())
}

// fakeLLM is a stub llm.Client that records forwarded requests.
type fakeLLM struct {
	forwardResp []byte
	forwardErr  error
	gotPath     string
	gotBody     []byte
}

func (f *fakeLLM) ListModels(ctx context.Context) (*llm.ListModelsResponse, error) { return nil, nil }
func (f *fakeLLM) ForwardRequest(ctx context.Context, path string, body []byte, stream bool) (*http.Response, error) {
	f.gotPath, f.gotBody = path, body
	if f.forwardErr != nil {
		return nil, f.forwardErr
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(f.forwardResp)),
	}, nil
}

// nodeStub is a node the relay must never call: serving a request makes no
// call to the node (decision DC1).
func nodeStub(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the relay called the node: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newTestServer(nodeURL string, llmClient llm.Client) *Server {
	cfg := &config.Config{}
	cfg.Provider.URL = nodeURL
	cfg.Provider.APIKey = "test-key"
	cfg.Provider.Engine = "vllm"
	s := &Server{config: cfg, llmClient: llmClient}
	s.SetMaxInflight(1)
	return s
}

func postChat(s *Server, hmac, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if hmac != "" {
		req.Header.Set("X-Request-Id", hmac)
	}
	rec := httptest.NewRecorder()
	s.handleChatCompletions(rec, req)
	return rec
}

func TestWriteDeadlineOnRealServer(t *testing.T) {
	node := nodeStub(t)
	s := newTestServer(node.URL, &sseLLM{body: "data: [DONE]\n\n"})
	s.config.Provider.Model = "m"
	r := mux.NewRouter()
	r.HandleFunc("/v1/chat/completions", s.handleChatCompletions).Methods(http.MethodPost)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	req, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Id", "good-hmac")
	req.Header.Set("X-Inferoute-Timeout-Ms", "600000")
	// A time of day from another machine's clock is not used. This one is in
	// the past, which used to fail every write.
	req.Header.Set("X-Inferoute-Deadline", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("body = %q", body)
	}
}

func TestStreamWriteDeadline(t *testing.T) {
	arrived := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	when, ok := streamWriteDeadline("1800000", arrived)
	if !ok || !when.Equal(arrived.Add(30*time.Minute+streamWriteDeadlineGrace)) {
		t.Fatalf("deadline = %v ok = %v", when, ok)
	}
	for _, header := range []string{"", "abc", "0", "-5", "2026-01-02T03:04:05Z"} {
		if _, ok := streamWriteDeadline(header, arrived); ok {
			t.Errorf("header %q set a deadline", header)
		}
	}
}

func TestEngineStatusPassedThrough(t *testing.T) {
	node := nodeStub(t)
	s := newTestServer(node.URL, &statusLLM{code: http.StatusBadRequest, body: `{"error":"maximum context length"}`})
	rec := postChat(s, "good-hmac", `{"model":"m","stream":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "maximum context length") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

type statusLLM struct {
	code int
	body string
}

func (s *statusLLM) ListModels(context.Context) (*llm.ListModelsResponse, error) { return nil, nil }
func (s *statusLLM) ForwardRequest(context.Context, string, []byte, bool) (*http.Response, error) {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: s.code, Header: h, Body: io.NopCloser(strings.NewReader(s.body))}, nil
}

func TestHandleChatCompletionsStreamsSSE(t *testing.T) {
	node := nodeStub(t)
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\n"
	s := newTestServer(node.URL, &sseLLM{body: sse})

	rec := postChat(s, "good-hmac", `{"model":"m","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Flushed {
		// httptest.ResponseRecorder.Flushed is true after Flush.
	} else {
		t.Fatal("response was not flushed")
	}
}

type sseLLM struct{ body string }

func (s *sseLLM) ListModels(ctx context.Context) (*llm.ListModelsResponse, error) {
	return nil, nil
}
func (s *sseLLM) ForwardRequest(ctx context.Context, path string, body []byte, stream bool) (*http.Response, error) {
	h := make(http.Header)
	h.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func TestHandleChatCompletionsGuardChain(t *testing.T) {
	t.Run("a request is forwarded to the engine and its answer returned", func(t *testing.T) {
		node := nodeStub(t)
		fake := &fakeLLM{forwardResp: []byte(`{"ok":true}`)}
		s := newTestServer(node.URL, fake)

		rec := postChat(s, "good-hmac", `{"model":"m"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.String() != `{"ok":true}` {
			t.Fatalf("body = %q, want forwarded LLM response", rec.Body.String())
		}
		if fake.gotPath != "/v1/chat/completions" {
			t.Fatalf("forwarded path = %q", fake.gotPath)
		}
	})

	t.Run("embeddings forwards to /v1/embeddings", func(t *testing.T) {
		node := nodeStub(t)
		fake := &fakeLLM{forwardResp: []byte(`{"data":[{"embedding":[0.1,0.2]}]}`)}
		s := newTestServer(node.URL, fake)
		req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"baai/bge-m3","input":"hi"}`))
		req.Header.Set("X-Request-Id", "good-hmac")
		rec := httptest.NewRecorder()
		s.handleEmbeddings(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if fake.gotPath != "/v1/embeddings" {
			t.Fatalf("forwarded path = %q", fake.gotPath)
		}
	})

}

func TestVerifyModelInRequestNilVerifierPasses(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	if err := s.verifyModel(context.Background(), "m"); err != nil {
		t.Fatalf("nil verifier should pass, got %v", err)
	}
}

type blockingLLM struct {
	started chan struct{}
	release chan struct{}
	inner   fakeLLM
}

func (b *blockingLLM) ListModels(ctx context.Context) (*llm.ListModelsResponse, error) {
	return b.inner.ListModels(ctx)
}
func (b *blockingLLM) ForwardRequest(ctx context.Context, path string, body []byte, stream bool) (*http.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	return b.inner.ForwardRequest(ctx, path, body, stream)
}

func TestInflightCapRejectsConcurrentInference(t *testing.T) {
	node := nodeStub(t)
	block := &blockingLLM{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		inner:   fakeLLM{forwardResp: []byte(`{"ok":true}`)},
	}
	s := newTestServer(node.URL, block)

	done := make(chan int, 1)
	go func() {
		done <- postChat(s, "hmac", `{"model":"m"}`).Code
	}()

	select {
	case <-block.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached LLM")
	}

	busyRec := httptest.NewRecorder()
	s.handleBusy(busyRec, httptest.NewRequest(http.MethodGet, "/api/busy", nil))
	if busyRec.Code != http.StatusOK {
		t.Fatalf("busy status = %d", busyRec.Code)
	}
	var busy BusyResponse
	if err := json.NewDecoder(busyRec.Body).Decode(&busy); err != nil {
		t.Fatalf("decode busy: %v", err)
	}
	if !busy.Busy {
		t.Fatal("expected busy while inference in flight")
	}

	if got := postChat(s, "hmac", `{"model":"m"}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("second request status = %d, want 503", got)
	}

	close(block.release)
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("first request status = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not finish")
	}

	if got := postChat(s, "hmac", `{"model":"m"}`).Code; got != http.StatusOK {
		t.Fatalf("after release status = %d, want 200", got)
	}
}

func TestSameSessionRequestQueuesInsteadOf503(t *testing.T) {
	node := nodeStub(t)
	block := &blockingLLM{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		inner:   fakeLLM{forwardResp: []byte(`{"ok":true}`)},
	}
	s := newTestServer(node.URL, block)
	s.sessionQueueWait = 5 * time.Second

	postSession := func(sessionKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("X-Request-Id", "hmac")
		req.Header.Set("X-Session-Key", sessionKey)
		rec := httptest.NewRecorder()
		s.handleChatCompletions(rec, req)
		return rec
	}

	first := make(chan int, 1)
	go func() { first <- postSession("sess-1").Code }()
	select {
	case <-block.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached LLM")
	}

	// Different or missing session key while busy: immediate 503 as before.
	if got := postSession("other-session").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("different-session status = %d, want 503", got)
	}
	if got := postChat(s, "hmac", `{"model":"m"}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("no-session status = %d, want 503", got)
	}

	// Same session key: queues for the slot instead of failing.
	second := make(chan int, 1)
	go func() { second <- postSession("sess-1").Code }()

	select {
	case code := <-second:
		t.Fatalf("same-session request returned %d while slot was held, expected it to wait", code)
	case <-time.After(300 * time.Millisecond):
	}

	close(block.release)
	for i, ch := range []chan int{first, second} {
		select {
		case code := <-ch:
			if code != http.StatusOK {
				t.Fatalf("request %d status = %d, want 200", i+1, code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("request %d did not finish", i+1)
		}
	}
}

func TestBenchmarkingRejectsAfterAcquire(t *testing.T) {
	node := nodeStub(t)
	block := &blockingLLM{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		inner:   fakeLLM{forwardResp: []byte(`{"ok":true}`)},
	}
	s := newTestServer(node.URL, block)
	s.sessionQueueWait = 5 * time.Second

	postSession := func(sessionKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("X-Request-Id", "hmac")
		req.Header.Set("X-Session-Key", sessionKey)
		rec := httptest.NewRecorder()
		s.handleChatCompletions(rec, req)
		return rec
	}

	first := make(chan int, 1)
	go func() { first <- postSession("sess-1").Code }()
	select {
	case <-block.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached LLM")
	}

	second := make(chan int, 1)
	go func() { second <- postSession("sess-1").Code }()
	time.Sleep(200 * time.Millisecond)

	s.SetBenchmarking(true)
	close(block.release)

	select {
	case code := <-first:
		if code != http.StatusOK {
			t.Fatalf("first request status = %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not finish")
	}
	select {
	case code := <-second:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("queued request status = %d, want 503 after benchmark gate", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued request did not finish")
	}
	if n, _, _ := s.SlotSnapshot(); n != 0 {
		t.Fatalf("inflight = %d, want 0 after gated waiter released the slot", n)
	}
}

func TestBenchmarkingRejectsNewInference(t *testing.T) {
	node := nodeStub(t)
	s := newTestServer(node.URL, &fakeLLM{forwardResp: []byte(`{"ok":true}`)})
	s.SetBenchmarking(true)
	if got := postChat(s, "hmac", `{"model":"m"}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
	if n, _, _ := s.SlotSnapshot(); n != 0 {
		t.Fatalf("inflight = %d, want 0", n)
	}
}

func TestSameSessionQueueTimesOut(t *testing.T) {
	node := nodeStub(t)
	block := &blockingLLM{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		inner:   fakeLLM{forwardResp: []byte(`{"ok":true}`)},
	}
	s := newTestServer(node.URL, block)
	s.sessionQueueWait = 300 * time.Millisecond

	postSession := func(sessionKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("X-Request-Id", "hmac")
		req.Header.Set("X-Session-Key", sessionKey)
		rec := httptest.NewRecorder()
		s.handleChatCompletions(rec, req)
		return rec
	}

	first := make(chan int, 1)
	go func() { first <- postSession("sess-1").Code }()
	select {
	case <-block.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached LLM")
	}

	// The slot never frees within the bounded wait: 503 so the orchestrator
	// can fail over.
	if got := postSession("sess-1").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("queued request status = %d, want 503 after wait timeout", got)
	}

	close(block.release)
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not finish")
	}
}
