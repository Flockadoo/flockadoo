// Package fakeengine is an inference engine for tests: an in-process HTTP
// server that speaks vLLM's or llama.cpp's surface (plan section 17.1).
//
// A test scripts the next answers with Next; nothing is triggered by magic
// words in a prompt. The engine records every inference request, whether the
// client went away before the answer was complete, and the most requests it
// had in progress at once.
package fakeengine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Flavour is the engine an Engine imitates.
type Flavour string

const (
	FlavourVLLM     Flavour = "vllm"
	FlavourLlamaCpp Flavour = "llama_cpp"
)

// Engine is a running fake engine. It stops when the test ends.
type Engine struct {
	URL string

	srv     *httptest.Server
	flavour Flavour

	mu          sync.Mutex
	model       string
	maxModelLen int64
	version     string
	embedding   bool
	ready       bool
	waiting     int32
	kvUsage     float64
	script      []Response
	requests    []*Request
	inFlight    int
	maxInFlight int
	changed     chan struct{}
}

// Option configures an Engine.
type Option func(*Engine)

// Model sets the id the engine serves (default "fake/model").
func Model(id string) Option { return func(e *Engine) { e.model = id } }

// MaxModelLen sets the context length the engine reports (default 8192).
func MaxModelLen(n int64) Option { return func(e *Engine) { e.maxModelLen = n } }

// Version sets the engine version: vLLM's "0.33.0", llama.cpp's build tag "b12359".
func Version(v string) Option { return func(e *Engine) { e.version = v } }

// Embedding makes the engine serve an embedding model: /v1/embeddings
// answers, chat and completions are refused with 400.
func Embedding() Option { return func(e *Engine) { e.embedding = true } }

// VLLM starts an engine that answers like vLLM (and vLLM Metal).
func VLLM(t testing.TB, opts ...Option) *Engine {
	return start(t, FlavourVLLM, "0.33.0", opts)
}

// LlamaCpp starts an engine that answers like llama-server.
func LlamaCpp(t testing.TB, opts ...Option) *Engine {
	return start(t, FlavourLlamaCpp, "b12359", opts)
}

func start(t testing.TB, f Flavour, version string, opts []Option) *Engine {
	t.Helper()
	e := &Engine{
		flavour:     f,
		model:       "fake/model",
		maxModelLen: 8192,
		version:     version,
		ready:       true,
		changed:     make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	e.srv = httptest.NewServer(http.HandlerFunc(e.serveHTTP))
	e.URL = e.srv.URL
	// Cut open connections first: a stalled or hung answer whose client never
	// left would otherwise keep Close waiting, and a failing test would hang.
	t.Cleanup(func() {
		e.srv.CloseClientConnections()
		e.srv.Close()
	})
	return e
}

// Flavour is the engine this one imitates.
func (e *Engine) Flavour() Flavour { return e.flavour }

// Next queues answers for the next inference requests, in order. With
// nothing queued, a request gets a short completion, streamed if it asked
// for a stream.
func (e *Engine) Next(rs ...Response) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.script = append(e.script, rs...)
}

// SetVersion changes the version the engine reports.
func (e *Engine) SetVersion(v string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.version = v
}

// SetReady sets whether the engine has finished loading. While it has not,
// /health and /v1/models answer 503, as both engines do.
func (e *Engine) SetReady(ready bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ready = ready
}

// SetMetrics sets the queue length and KV cache use /metrics reports.
func (e *Engine) SetMetrics(waiting int32, kvCacheUsage float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.waiting, e.kvUsage = waiting, kvCacheUsage
}

// Request is one inference request the engine received.
type Request struct {
	Path   string
	Header http.Header
	Body   []byte
	// Cancelled is true when the client went away before the answer was
	// complete (a closed connection or a cancelled context).
	Cancelled bool
}

// Requests returns every inference request so far, in arrival order.
func (e *Engine) Requests() []Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Request, len(e.requests))
	for i, r := range e.requests {
		out[i] = *r
	}
	return out
}

// InFlight is the number of inference requests being answered now.
func (e *Engine) InFlight() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inFlight
}

// MaxInFlight is the most inference requests the engine answered at once.
func (e *Engine) MaxInFlight() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxInFlight
}

// ResetMaxInFlight starts a new high-water mark from the requests in
// progress now, for a test that measures a second phase on its own.
func (e *Engine) ResetMaxInFlight() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.maxInFlight = e.inFlight
}

// AwaitIdle waits until no inference request is in progress.
func (e *Engine) AwaitIdle(t testing.TB) {
	t.Helper()
	e.await(t, "no request in flight", func() bool { return e.inFlight == 0 })
}

// AwaitInFlight waits until n inference requests are in progress at once.
func (e *Engine) AwaitInFlight(t testing.TB, n int) {
	t.Helper()
	e.await(t, fmt.Sprintf("%d requests in flight", n), func() bool { return e.inFlight >= n })
}

func (e *Engine) await(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		e.mu.Lock()
		ok, changed := cond(), e.changed
		e.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("fakeengine: timed out waiting for %s", what)
		}
	}
}

// notify wakes every waiter. Call it with e.mu held.
func (e *Engine) notify() {
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *Engine) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/completions" || r.URL.Path == "/v1/embeddings"):
		e.inference(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		e.models(w)
	case r.Method == http.MethodGet && r.URL.Path == "/health":
		e.health(w)
	case r.Method == http.MethodGet && r.URL.Path == "/metrics":
		e.metrics(w)
	case r.Method == http.MethodGet && r.URL.Path == "/version" && e.flavour == FlavourVLLM:
		e.mu.Lock()
		v := e.version
		e.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"version": v})
	case r.Method == http.MethodGet && r.URL.Path == "/props" && e.flavour == FlavourLlamaCpp:
		e.mu.Lock()
		props := map[string]any{
			"default_generation_settings": map[string]any{"n_ctx": e.maxModelLen},
			"total_slots":                 4,
			"model_alias":                 e.model,
			"build_info":                  e.version + "-0fa4e00",
			"is_sleeping":                 false,
		}
		e.mu.Unlock()
		writeJSON(w, http.StatusOK, props)
	default:
		http.NotFound(w, r)
	}
}

func (e *Engine) models(w http.ResponseWriter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.ready {
		e.writeLoading(w)
		return
	}
	entry := map[string]any{"id": e.model, "object": "model", "owned_by": "vllm", "max_model_len": e.maxModelLen}
	if e.flavour == FlavourLlamaCpp {
		entry = map[string]any{"id": e.model, "object": "model", "owned_by": "llamacpp", "meta": map[string]any{"n_ctx": e.maxModelLen}}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{entry}})
}

func (e *Engine) health(w http.ResponseWriter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case !e.ready:
		e.writeLoading(w)
	case e.flavour == FlavourLlamaCpp:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// writeLoading is what each engine answers while its model loads.
func (e *Engine) writeLoading(w http.ResponseWriter) {
	if e.flavour == FlavourLlamaCpp {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": 503, "message": "Loading model", "type": "unavailable_error"}})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "engine is starting"})
}

func (e *Engine) metrics(w http.ResponseWriter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	if e.flavour == FlavourLlamaCpp {
		_, _ = fmt.Fprintf(w, "# TYPE llamacpp:requests_deferred gauge\nllamacpp:requests_deferred %d\n# TYPE llamacpp:kv_cache_usage_ratio gauge\nllamacpp:kv_cache_usage_ratio %g\n", e.waiting, e.kvUsage)
		return
	}
	_, _ = fmt.Fprintf(w, "# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting{model_name=%q} %d\n# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc{model_name=%q} %g\n", e.model, e.waiting, e.model, e.kvUsage)
}

func (e *Engine) inference(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := &Request{Path: r.URL.Path, Header: r.Header.Clone(), Body: body}

	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.inFlight++
	if e.inFlight > e.maxInFlight {
		e.maxInFlight = e.inFlight
	}
	var resp Response
	if len(e.script) > 0 {
		resp, e.script = e.script[0], e.script[1:]
	} else {
		resp = e.defaultResponse(r.URL.Path, body)
	}
	model, embedding := e.model, e.embedding
	e.notify()
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		if r.Context().Err() != nil {
			req.Cancelled = true
		}
		e.inFlight--
		e.notify()
		e.mu.Unlock()
	}()

	if embedding != (r.URL.Path == "/v1/embeddings") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "this model does not support " + r.URL.Path, "type": "BadRequestError", "code": 400}})
		return
	}
	resp.serve(e, w, r, model)
}

// defaultResponse answers a request nothing was scripted for. Call it with
// e.mu held.
func (e *Engine) defaultResponse(path string, body []byte) Response {
	if path == "/v1/embeddings" {
		return embeddingResponse()
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Stream {
		return Stream("ok")
	}
	return Complete("ok", Usage{PromptTokens: 1, CompletionTokens: 1})
}

// crash closes the listener and every connection, so this request and every
// later one fails.
func (e *Engine) crash() {
	_ = e.srv.Listener.Close()
	e.srv.CloseClientConnections()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Usage is the token count an answer reports.
type Usage struct{ PromptTokens, CompletionTokens int }

// Response is one scripted answer. Build it with the functions below; Slow,
// Partial and Stall change how a stream is delivered.
type Response struct {
	build       func(path, model string) (status int, contentType string, body []byte, events [][]byte)
	gap         time.Duration
	delay       time.Duration // wait before the first byte
	cutAfter    int           // abort the connection after this many events; -1 never
	stallAfter  int           // hold the stream open after this many events; -1 never
	hang, crash bool
}

func newResponse(build func(path, model string) (int, string, []byte, [][]byte)) Response {
	return Response{build: build, cutAfter: -1, stallAfter: -1}
}

// Slow waits perChunk between the events of a stream.
func (r Response) Slow(perChunk time.Duration) Response { r.gap = perChunk; return r }

// Delay waits before the first byte. A non-streaming answer does this for
// the whole generation: nothing is written until it is finished.
func (r Response) Delay(d time.Duration) Response { r.delay = d; return r }

// Partial sends n events, then drops the connection with no [DONE] and no
// clean end of the body.
func (r Response) Partial(n int) Response { r.cutAfter = n; return r }

// Stall sends n events, then nothing, holding the connection open until
// the client goes away.
func (r Response) Stall(n int) Response { r.stallAfter = n; return r }

func (r Response) serve(e *Engine, w http.ResponseWriter, req *http.Request, model string) {
	ctx := req.Context()
	if r.hang {
		<-ctx.Done()
		return
	}
	if r.crash {
		e.crash()
		panic(http.ErrAbortHandler)
	}
	status, contentType, body, events := r.build(req.URL.Path, model)
	if r.delay > 0 {
		timer := time.NewTimer(r.delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if events == nil {
		_, _ = w.Write(body)
		return
	}
	rc := http.NewResponseController(w)
	for i := 0; ; i++ {
		if i == r.cutAfter {
			_ = rc.Flush()
			panic(http.ErrAbortHandler)
		}
		if i == r.stallAfter {
			<-ctx.Done()
			return
		}
		if i == len(events) {
			return
		}
		if i > 0 && r.gap > 0 {
			timer := time.NewTimer(r.gap)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		if _, err := w.Write(events[i]); err != nil {
			return
		}
		_ = rc.Flush()
	}
}

// Complete answers with one JSON completion: a chat message on
// /v1/chat/completions, text on /v1/completions.
func Complete(text string, usage Usage) Response {
	return newResponse(func(path, model string) (int, string, []byte, [][]byte) {
		choice := map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}
		object := "chat.completion"
		if path == "/v1/completions" {
			choice = map[string]any{"index": 0, "finish_reason": "stop", "text": text}
			object = "text_completion"
		}
		b, _ := json.Marshal(map[string]any{
			"id": "cmpl-fake", "object": object, "created": 0, "model": model,
			"choices": []any{choice},
			"usage":   usageJSON(usage),
		})
		return http.StatusOK, "application/json", b, nil
	})
}

// Stream answers with one SSE event per chunk, then a usage event, then
// [DONE], the way vLLM streams with stream_options.include_usage.
func Stream(chunks ...string) Response {
	return streamOf(chunks, &Usage{PromptTokens: 1, CompletionTokens: len(chunks)})
}

// NoUsage streams the chunks and [DONE] with no usage event.
func NoUsage(chunks ...string) Response { return streamOf(chunks, nil) }

// InflatedUsage streams the chunks with a usage event claiming
// completionTokens, far more than were generated. The client must pass it
// through unchanged; judging it is the node's job.
func InflatedUsage(completionTokens int, chunks ...string) Response {
	return streamOf(chunks, &Usage{PromptTokens: 1, CompletionTokens: completionTokens})
}

func streamOf(chunks []string, usage *Usage) Response {
	return newResponse(func(path, model string) (int, string, []byte, [][]byte) {
		var events [][]byte
		for i, c := range chunks {
			var finish any
			if i == len(chunks)-1 {
				finish = "stop"
			}
			choice := map[string]any{"index": 0, "delta": map[string]any{"content": c}, "finish_reason": finish}
			object := "chat.completion.chunk"
			if path == "/v1/completions" {
				choice = map[string]any{"index": 0, "text": c, "finish_reason": finish}
				object = "text_completion"
			}
			events = append(events, sseEvent(map[string]any{"id": "cmpl-fake", "object": object, "created": 0, "model": model, "choices": []any{choice}}))
		}
		if usage != nil {
			events = append(events, sseEvent(map[string]any{"id": "cmpl-fake", "object": "chat.completion.chunk", "created": 0, "model": model, "choices": []any{}, "usage": usageJSON(*usage)}))
		}
		events = append(events, []byte("data: [DONE]\n\n"))
		return http.StatusOK, "text/event-stream; charset=utf-8", nil, events
	})
}

func sseEvent(v any) []byte {
	b, _ := json.Marshal(v)
	return append(append([]byte("data: "), b...), '\n', '\n')
}

func usageJSON(u Usage) map[string]int {
	return map[string]int{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens, "total_tokens": u.PromptTokens + u.CompletionTokens}
}

// Replay answers with a recording from the corpus, byte for byte, with its
// recorded Content-Type. recording is "<folder>/<case>", for example
// "vllm-0.28.0-qwen2.5-3b-instruct/chat-plain". Each SSE event is flushed on
// its own, as the engine sent it.
func Replay(t testing.TB, recording string) Response {
	t.Helper()
	body, contentType, err := loadRecording(recording)
	if err != nil {
		t.Fatalf("fakeengine: %v", err)
	}
	var events [][]byte
	for len(body) > 0 {
		i := bytes.Index(body, []byte("\n\n"))
		if i < 0 {
			events = append(events, body)
			break
		}
		events = append(events, body[:i+2])
		body = body[i+2:]
	}
	return newResponse(func(string, string) (int, string, []byte, [][]byte) {
		return http.StatusOK, contentType, nil, events
	})
}

// InvalidJSON answers 200 with a body that is not JSON.
func InvalidJSON() Response {
	return newResponse(func(string, string) (int, string, []byte, [][]byte) {
		return http.StatusOK, "application/json", []byte(`{"id":"cmpl-fake","choices":[{"message":`), nil
	})
}

// MalformedSSE answers with a stream that breaks the format: a line without
// "data:", an event cut mid-JSON, and two events with no blank line between.
func MalformedSSE() Response {
	return newResponse(func(string, string) (int, string, []byte, [][]byte) {
		return http.StatusOK, "text/event-stream", nil, [][]byte{
			[]byte(`data: {"id":"cmpl-fake","choices":[{"index":0,"delta":{"content":"a"}}]}` + "\n\n"),
			[]byte("this line has no data prefix\n\n"),
			[]byte(`data: {"id":"cmpl-fake","choices":[{"index":0,"delta":{"cont` + "\n\n"),
			[]byte(`data: {"id":"cmpl-fake","choices":[]}` + "\ndata: [DONE]\n\n"),
		}
	})
}

// Status answers with code and body, as an engine does for an unsupported
// parameter (400) or when it is out of capacity (503).
func Status(code int, body string) Response {
	return newResponse(func(string, string) (int, string, []byte, [][]byte) {
		return code, "application/json", []byte(body), nil
	})
}

// Hang accepts the request and never answers.
func Hang() Response { r := newResponse(nil); r.hang = true; return r }

// Crash kills the engine: this request's connection drops, and every later
// connection is refused.
func Crash() Response { r := newResponse(nil); r.crash = true; return r }

func embeddingResponse() Response {
	return newResponse(func(_, model string) (int, string, []byte, [][]byte) {
		b, _ := json.Marshal(map[string]any{
			"object": "list", "model": model,
			"data":  []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}},
			"usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1},
		})
		return http.StatusOK, "application/json", b, nil
	})
}

func contentTypeOf(headers []byte) string {
	for _, line := range strings.Split(string(headers), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "content-type") {
			return strings.TrimSpace(value)
		}
	}
	return "text/event-stream"
}
