package fakeengine_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
)

// Plan item A2, section 17.1: the fake engine's behaviours. The engine-facing
// checks go through the client's own engine client (pkg/llm), so the fake
// answers in the shapes the client actually reads.

func TestVLLMServesItsModelVersionAndMetrics(t *testing.T) {
	e := fakeengine.VLLM(t, fakeengine.Model("Qwen/Qwen2.5-3B-Instruct"), fakeengine.MaxModelLen(16384), fakeengine.Version("0.28.0"))
	e.SetMetrics(3, 0.5)
	c := llm.NewVLLMClient(e.URL, 5*time.Second).(*llm.VLLMClient)
	ctx := context.Background()

	models, err := c.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Models) != 1 || models.Models[0].ID != "Qwen/Qwen2.5-3B-Instruct" || models.Models[0].MaxModelLen != 16384 {
		t.Fatalf("models = %+v, want the one model with max_model_len 16384", models.Models)
	}
	if v := c.EngineVersion(ctx, false); v != "0.28.0" {
		t.Fatalf("version = %q, want 0.28.0", v)
	}
	got, err := c.EngineCapacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Waiting == nil || *got.Waiting != 3 || got.KVCacheUsage == nil || *got.KVCacheUsage != 0.5 {
		t.Fatalf("capacity = %+v, want waiting 3 and KV cache 0.5", got)
	}

	e.SetVersion("0.33.0")
	if v := c.EngineVersion(ctx, false); v != "0.33.0" {
		t.Fatalf("version after SetVersion = %q, want 0.33.0", v)
	}
}

func TestLlamaCppServesHealthPropsAndMetrics(t *testing.T) {
	e := fakeengine.LlamaCpp(t, fakeengine.Model("qwen"), fakeengine.MaxModelLen(8192), fakeengine.Version("b12359"))
	e.SetMetrics(2, 0.25)
	c := llm.NewVLLMClient(e.URL, 5*time.Second).(*llm.VLLMClient)
	ctx := context.Background()

	if status, body := get(t, e.URL+"/health"); status != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("/health = %d %s, want 200 with status ok", status, body)
	}
	if v := c.EngineVersion(ctx, true); v != "b12359" {
		t.Fatalf("version = %q, want b12359", v)
	}
	n, err := c.LlamaServerContext(ctx)
	if err != nil || n != 8192 {
		t.Fatalf("n_ctx = %d, %v; want 8192", n, err)
	}
	got, err := c.EngineCapacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Waiting == nil || *got.Waiting != 2 || got.KVCacheUsage == nil || *got.KVCacheUsage != 0.25 {
		t.Fatalf("capacity = %+v, want deferred 2 and KV cache 0.25", got)
	}
	if status, _ := get(t, e.URL+"/version"); status != http.StatusNotFound {
		t.Fatalf("llama.cpp answered /version with %d; the real server has no such route", status)
	}
}

func TestAnEngineThatIsNotReadyAnswers503(t *testing.T) {
	for _, e := range []*fakeengine.Engine{fakeengine.VLLM(t), fakeengine.LlamaCpp(t)} {
		e.SetReady(false)
		for _, path := range []string{"/health", "/v1/models"} {
			if status, _ := get(t, e.URL+path); status != http.StatusServiceUnavailable {
				t.Errorf("%s %s while loading = %d, want 503", e.Flavour(), path, status)
			}
		}
		e.SetReady(true)
		if status, _ := get(t, e.URL+"/health"); status != http.StatusOK {
			t.Errorf("%s /health when ready = %d, want 200", e.Flavour(), status)
		}
	}
}

func TestCompleteAnswersWithOneJSONCompletion(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Complete("hello", fakeengine.Usage{PromptTokens: 5, CompletionTokens: 2}))
	status, body := post(t, e.URL+"/v1/chat/completions", `{"model":"m","messages":[]}`)
	var got struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("got %d %s", status, body)
	}
	if len(got.Choices) != 1 || got.Choices[0].Message.Content != "hello" || got.Usage.PromptTokens != 5 || got.Usage.CompletionTokens != 2 {
		t.Fatalf("completion = %s", body)
	}
}

func TestStreamSendsChunksThenUsageThenDone(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Stream("a", "b", "c"))
	_, body := post(t, e.URL+"/v1/chat/completions", `{"model":"m","stream":true}`)
	events := sseData(body)
	if len(events) != 5 {
		t.Fatalf("events = %q, want three chunks, a usage chunk and [DONE]", events)
	}
	for i, want := range []string{"a", "b", "c"} {
		if !strings.Contains(events[i], `"content":"`+want+`"`) {
			t.Errorf("event %d = %s, want content %q", i, events[i], want)
		}
	}
	if !strings.Contains(events[3], `"usage"`) || events[4] != "[DONE]" {
		t.Fatalf("stream end = %q, want usage then [DONE]", events[3:])
	}
}

func TestWithNothingScriptedTheAnswerFollowsTheStreamFlag(t *testing.T) {
	e := fakeengine.VLLM(t)
	_, plain := post(t, e.URL+"/v1/chat/completions", `{"model":"m"}`)
	if !json.Valid([]byte(plain)) {
		t.Fatalf("an unscripted non-stream request got %q, want JSON", plain)
	}
	_, streamed := post(t, e.URL+"/v1/chat/completions", `{"model":"m","stream":true}`)
	if ev := sseData(streamed); len(ev) == 0 || ev[len(ev)-1] != "[DONE]" {
		t.Fatalf("an unscripted stream request got %q, want an SSE stream ending in [DONE]", streamed)
	}
}

func TestReplaySendsTheRecordingByteForByte(t *testing.T) {
	const rec = "vllm-0.28.0-qwen2.5-3b-instruct/chat-plain"
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Replay(t, rec))
	resp, err := http.Post(e.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(fakeengine.CorpusDir(), filepath.FromSlash(rec)+".sse"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("replayed %d bytes, the recording has %d; they differ", len(got), len(want))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want the recorded text/event-stream", ct)
	}
}

func TestSlowSpacesTheChunks(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Stream("a", "b", "c").Slow(40 * time.Millisecond))
	start := time.Now()
	post(t, e.URL+"/v1/chat/completions", `{"stream":true}`)
	if d := time.Since(start); d < 120*time.Millisecond {
		t.Fatalf("five events with 40ms between them took %v", d)
	}
}

func TestPartialEndsTheConnectionWithoutDone(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Stream("a", "b", "c").Partial(1))
	resp, err := http.Post(e.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("a cut stream read to a clean end: %q", body)
	}
	if strings.Contains(string(body), "[DONE]") || !strings.Contains(string(body), `"content":"a"`) {
		t.Fatalf("body = %q, want the first chunk and no [DONE]", body)
	}
}

func TestStallHoldsTheStreamUntilTheClientLeaves(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Stream("a", "b").Stall(1))
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, `"content":"a"`) {
		t.Fatalf("first line = %q, %v", line, err)
	}
	if e.InFlight() != 1 {
		t.Fatalf("in flight while stalled = %d, want 1", e.InFlight())
	}
	cancel()
	e.AwaitIdle(t)
	reqs := e.Requests()
	if len(reqs) != 1 || !reqs[0].Cancelled {
		t.Fatalf("requests = %+v, want one, cancelled by the client", reqs)
	}
}

func TestBrokenBodies(t *testing.T) {
	cases := []struct {
		name  string
		r     fakeengine.Response
		check func(body string) bool
	}{
		{"invalid JSON", fakeengine.InvalidJSON(), func(b string) bool { return b != "" && !json.Valid([]byte(b)) }},
		{"malformed SSE", fakeengine.MalformedSSE(), func(b string) bool {
			return strings.Contains(b, "data: {\"id\"") && hasLineWithout(b, "data:")
		}},
		{"no usage", fakeengine.NoUsage("a"), func(b string) bool {
			return strings.Contains(b, "[DONE]") && !strings.Contains(b, `"usage"`)
		}},
		{"inflated usage", fakeengine.InflatedUsage(100000, "a"), func(b string) bool {
			return strings.Contains(b, `"completion_tokens":100000`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := fakeengine.VLLM(t)
			e.Next(tc.r)
			if _, body := post(t, e.URL+"/v1/chat/completions", `{"stream":true}`); !tc.check(body) {
				t.Fatalf("body = %q", body)
			}
		})
	}
}

func TestStatusPassesTheCodeAndBody(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Status(http.StatusBadRequest, `{"error":"unsupported parameter"}`))
	status, body := post(t, e.URL+"/v1/chat/completions", `{}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "unsupported parameter") {
		t.Fatalf("got %d %s", status, body)
	}
}

// Delay holds the whole answer, status line included, as a plain answer
// does while it is generated; a client that gives up first cancels it.
func TestDelayHoldsTheFirstByte(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Complete("late", fakeengine.Usage{PromptTokens: 1, CompletionTokens: 1}).Delay(200 * time.Millisecond))
	start := time.Now()
	status, body := post(t, e.URL+"/v1/chat/completions", `{"model":"m","messages":[]}`)
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("answered after %v, before the 200ms delay", d)
	}
	if status != http.StatusOK || !strings.Contains(body, `"late"`) {
		t.Fatalf("got %d %s, want the completion after the delay", status, body)
	}

	e.Next(fakeengine.Complete("never", fakeengine.Usage{}).Delay(time.Minute))
	client := &http.Client{Timeout: 100 * time.Millisecond}
	resp, err := client.Post(e.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","messages":[]}`))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a delayed answer arrived before its delay")
	}
	e.AwaitIdle(t)
	if reqs := e.Requests(); len(reqs) != 2 || !reqs[1].Cancelled {
		t.Fatalf("requests = %+v, want the second cancelled when the client gave up", reqs)
	}
}

func TestHangNeverAnswers(t *testing.T) {
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Hang())
	client := &http.Client{Timeout: 150 * time.Millisecond}
	resp, err := client.Post(e.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a hung engine answered")
	}
	e.AwaitIdle(t)
	if reqs := e.Requests(); len(reqs) != 1 || !reqs[0].Cancelled {
		t.Fatalf("requests = %+v, want one, cancelled when the client gave up", reqs)
	}
}

func TestCrashRefusesEveryLaterConnection(t *testing.T) {
	e := fakeengine.VLLM(t)
	if status, _ := get(t, e.URL+"/v1/models"); status != http.StatusOK {
		t.Fatalf("before the crash /v1/models = %d, want 200", status)
	}
	e.Next(fakeengine.Crash())
	if resp, err := http.Post(e.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`)); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the request that crashed the engine got an answer")
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: time.Second}
	if resp, err := client.Get(e.URL + "/v1/models"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a crashed engine still accepts connections")
	}
}

func TestAnEmbeddingEngineRefusesChat(t *testing.T) {
	e := fakeengine.VLLM(t, fakeengine.Embedding())
	status, body := post(t, e.URL+"/v1/embeddings", `{"model":"m","input":"hi"}`)
	if status != http.StatusOK || !strings.Contains(body, `"embedding"`) {
		t.Fatalf("embeddings = %d %s", status, body)
	}
	if status, _ := post(t, e.URL+"/v1/chat/completions", `{"model":"m"}`); status != http.StatusBadRequest {
		t.Fatalf("chat on an embedding model = %d, want 400", status)
	}
}

func TestRecordsBodiesAndTheMostRequestsAtOnce(t *testing.T) {
	e := fakeengine.VLLM(t)
	const n = 3
	for range n {
		e.Next(fakeengine.Stream("x").Stall(1))
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.URL+"/v1/completions", strings.NewReader(`{"n":`+string(rune('0'+i))+`}`))
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
	}
	e.AwaitInFlight(t, n)
	cancel()
	wg.Wait()
	e.AwaitIdle(t)
	if e.MaxInFlight() != n {
		t.Fatalf("most at once = %d, want %d", e.MaxInFlight(), n)
	}
	reqs := e.Requests()
	if len(reqs) != n {
		t.Fatalf("recorded %d requests, want %d", len(reqs), n)
	}
	for _, r := range reqs {
		if r.Path != "/v1/completions" || !strings.HasPrefix(string(r.Body), `{"n":`) {
			t.Fatalf("recorded %s %q", r.Path, r.Body)
		}
	}
}

// The corpus is copied from inferoute-node with a checksum file; an edited
// or added file is caught here (the node's rule: recordings are never
// written by hand).
func TestTheCorpusMatchesItsChecksums(t *testing.T) {
	if err := fakeengine.VerifyCorpus(); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// sseData returns the payload of every data: line, in order.
func sseData(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func hasLineWithout(body, prefix string) bool {
	for _, line := range strings.Split(body, "\n") {
		if line != "" && !strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
