package capprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyStatus(t *testing.T) {
	for status, want := range map[int]Outcome{
		400: Unsupported,
		422: Unsupported,
		401: Unverified,
		403: Unverified,
		404: Unverified,
		408: Unverified,
		409: Unverified,
		429: Unverified,
		500: Unverified,
		503: Unverified,
	} {
		if got := classifyStatus(status); got != want {
			t.Errorf("status %d = %q, want %q", status, got, want)
		}
	}
}

func TestProbeAsksForARepeatableAnswer(t *testing.T) {
	body := probeBody("m", "json_schema")
	if body["temperature"] != 0 {
		t.Fatalf("temperature = %v", body["temperature"])
	}
	if n, _ := body["max_tokens"].(int); n < 1024 {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
}

// JSON mode needs the prompt to ask for JSON (OpenAI's guidance): without it
// a model may stream whitespace until max_tokens. DeepSeek-R1-Distill-Qwen-7B
// on vLLM 0.34 did that for "Reply with ok.", and answered valid JSON when
// asked for it. The schema probe names every key the schema requires.
func TestJSONProbesAskForJSON(t *testing.T) {
	ask := func(name string) string {
		msgs, _ := probeBody("m", name)["messages"].([]map[string]string)
		if len(msgs) != 1 {
			t.Fatalf("%s: messages = %v", name, msgs)
		}
		return msgs[0]["content"]
	}
	for _, name := range []string{"json_object", "json_schema"} {
		if text := ask(name); !strings.Contains(text, "JSON") {
			t.Errorf("%s probe asks %q, want it to ask for JSON", name, text)
		}
	}
	rf, _ := probeBody("m", "json_schema")["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	required, _ := schema["required"].([]string)
	if len(required) == 0 {
		t.Fatal("the schema probe requires no keys")
	}
	for _, key := range required {
		if text := ask("json_schema"); !strings.Contains(text, key) {
			t.Errorf("schema probe asks %q, want it to name the required key %q", text, key)
		}
	}
	// The other probes keep their prompts.
	if text := ask("logprobs"); text != "Reply with ok." {
		t.Errorf("logprobs probe asks %q", text)
	}
}

// probeName says which of the five checks an engine request is.
func probeName(body map[string]any) string {
	switch {
	case body["reasoning_effort"] != nil:
		return "reasoning"
	case body["tool_choice"] == "required":
		return "forced_tools"
	case body["tools"] != nil:
		return "tools"
	case body["logprobs"] == true:
		return "logprobs"
	}
	rf, _ := body["response_format"].(map[string]any)
	if rf["type"] == "json_schema" {
		return "json_schema"
	}
	return "json_object"
}

const (
	toolCallAnswer = `{"choices":[{"message":{"content":null,"tool_calls":[{"function":{"name":"get_current_weather","arguments":"{}"}}]}}]}`
	textAnswer     = `{"choices":[{"message":{"content":"It is sunny in Paris."}}]}`
)

func passingAnswer(name string) string {
	switch name {
	case "tools", "forced_tools":
		return toolCallAnswer
	case "logprobs":
		return `{"choices":[{"message":{"content":"ok"},"logprobs":{"content":[{"token":"ok"}]}}]}`
	case "reasoning":
		return vllmThinking
	default:
		return `{"choices":[{"message":{"content":"{\"ok\":true,\"name\":\"x\"}"}}]}`
	}
}

// fakeEngine passes every probe. tools is called for the "tools" probe with
// the number of times it has been asked, and returns the status and body.
func fakeEngine(t *testing.T, tools func(call int) (int, string)) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	toolCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := probeName(body)
		if name == "tools" && tools != nil {
			mu.Lock()
			toolCalls++
			n := toolCalls
			mu.Unlock()
			status, answer := tools(n)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(answer))
			return
		}
		_, _ = w.Write([]byte(passingAnswer(name)))
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return toolCalls
	}
}

// fakePlatform keeps the deployment row the way the API does.
type fakePlatform struct {
	mu        sync.Mutex
	dep       deployment
	registers int
	reports   []capabilityReport
}

func (p *fakePlatform) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.URL.Path == "/api/provider/capabilities/deployment" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(p.dep)
		case r.URL.Path == "/api/provider/capabilities/deployment":
			var req struct {
				Fingerprint string `json:"fingerprint"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			p.registers++
			p.dep = deployment{Revision: p.dep.Revision + 1, Fingerprint: req.Fingerprint}
			_ = json.NewEncoder(w).Encode(p.dep)
		case r.URL.Path == "/api/provider/capabilities":
			var rep capabilityReport
			_ = json.NewDecoder(r.Body).Decode(&rep)
			p.dep.Attempt = rep.Attempt
			p.reports = append(p.reports, rep)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (p *fakePlatform) last() (capabilityReport, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reports) == 0 {
		return capabilityReport{}, 0, p.registers
	}
	return p.reports[len(p.reports)-1], len(p.reports), p.registers
}

func runWatcher(t *testing.T, w *Watcher) {
	t.Helper()
	w.BusyWait, w.RetryWait, w.WatchWait = 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond
	w.Model, w.APIKey = "m", "k"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Loop(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func toolsFlag(p *fakePlatform) *bool {
	rep, _, _ := p.last()
	return rep.Tools
}

// fullReport is true once the last report carries all five flags.
func fullReport(p *fakePlatform) bool {
	rep, _, _ := p.last()
	return rep.Tools != nil && rep.ForcedTools != nil && rep.JSONObject != nil &&
		rep.JSONSchema != nil && rep.Logprobs != nil
}

// A busy engine (429) says nothing about tools. The probe is tried again.
func TestBusyEngineIsRetriedNotMarkedUnsupported(t *testing.T) {
	engine, _ := fakeEngine(t, func(call int) (int, string) {
		if call == 1 {
			return http.StatusTooManyRequests, `{"error":"busy"}`
		}
		return http.StatusOK, toolCallAnswer
	})
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
	})
	waitFor(t, "tools reported as supported", func() bool {
		v := toolsFlag(platform)
		return v != nil && *v
	})
	platform.mu.Lock()
	defer platform.mu.Unlock()
	for _, rep := range platform.reports {
		if rep.Tools != nil && !*rep.Tools {
			t.Fatal("a 429 was reported as tools unsupported")
		}
	}
}

// A model that answers in text once and then calls the tool supports tools.
func TestTextAnswerIsRetriedBeforeUnsupported(t *testing.T) {
	engine, _ := fakeEngine(t, func(call int) (int, string) {
		if call < softFailLimit {
			return http.StatusOK, textAnswer
		}
		return http.StatusOK, toolCallAnswer
	})
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
	})
	waitFor(t, "tools reported as supported", func() bool {
		v := toolsFlag(platform)
		return v != nil && *v
	})
}

// A model that never calls the tool is unsupported after softFailLimit tries.
func TestRepeatedTextAnswerIsUnsupported(t *testing.T) {
	engine, calls := fakeEngine(t, func(int) (int, string) { return http.StatusOK, textAnswer })
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
	})
	waitFor(t, "tools reported as unsupported", func() bool {
		v := toolsFlag(platform)
		return v != nil && !*v
	})
	if n := calls(); n != softFailLimit {
		t.Fatalf("tools probe ran %d times, want %d", n, softFailLimit)
	}
}

// An engine that rejects the feature (400) is unsupported on the first try.
func TestRejectedFeatureIsUnsupportedAtOnce(t *testing.T) {
	engine, calls := fakeEngine(t, func(int) (int, string) {
		return http.StatusBadRequest, `{"error":"tools are not enabled"}`
	})
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
	})
	waitFor(t, "tools reported as unsupported", func() bool {
		v := toolsFlag(platform)
		return v != nil && !*v
	})
	if n := calls(); n != 1 {
		t.Fatalf("tools probe ran %d times, want 1", n)
	}
}

// The watcher keeps running after a full report. A changed deployment is
// registered and probed again.
func TestChangedDeploymentIsProbedAgain(t *testing.T) {
	var mu sync.Mutex
	fp := "old"
	toolsOn := false
	engine, _ := fakeEngine(t, func(int) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if toolsOn {
			return http.StatusOK, toolCallAnswer
		}
		return http.StatusBadRequest, `{"error":"tools are not enabled"}`
	})
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string {
			mu.Lock()
			defer mu.Unlock()
			return fp
		},
	})
	waitFor(t, "first report", func() bool {
		v := toolsFlag(platform)
		return v != nil && !*v
	})

	// The provider restarts the engine with the tool parser on.
	mu.Lock()
	fp, toolsOn = "new", true
	mu.Unlock()
	waitFor(t, "re-probe after the change", func() bool {
		rep, _, registers := platform.last()
		return registers == 2 && rep.Fingerprint == "new" && rep.Tools != nil && *rep.Tools
	})
}

// One odd fingerprint reading does not reset the reported capabilities.
func TestFingerprintBlipDoesNotReRegister(t *testing.T) {
	var mu sync.Mutex
	blip := false
	engine, _ := fakeEngine(t, nil)
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string {
			mu.Lock()
			defer mu.Unlock()
			if blip {
				blip = false
				return "fp-without-gpu"
			}
			return "fp"
		},
	})
	waitFor(t, "first report", func() bool {
		_, reports, _ := platform.last()
		return reports == 1
	})
	mu.Lock()
	blip = true
	mu.Unlock()
	waitFor(t, "the blip to be read", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return !blip
	})
	time.Sleep(50 * time.Millisecond)
	if _, reports, registers := platform.last(); registers != 1 || reports != 1 {
		t.Fatalf("registers=%d reports=%d after one odd reading", registers, reports)
	}
}

// Nothing is probed until the engine can be identified.
func TestNoProbeWithoutFingerprint(t *testing.T) {
	engine, calls := fakeEngine(t, func(int) (int, string) { return http.StatusOK, toolCallAnswer })
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "" },
	})
	time.Sleep(50 * time.Millisecond)
	if n := calls(); n != 0 {
		t.Fatalf("probed %d times with no fingerprint", n)
	}
}

// Probes wait while the server is not idle (a request in flight, or the benchmark).
func TestNoProbeWhileBusy(t *testing.T) {
	var mu sync.Mutex
	idle := false
	engine, calls := fakeEngine(t, func(int) (int, string) { return http.StatusOK, toolCallAnswer })
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
		Idle: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return idle
		},
	})
	time.Sleep(50 * time.Millisecond)
	if n := calls(); n != 0 {
		t.Fatalf("probed %d times while busy", n)
	}
	mu.Lock()
	idle = true
	mu.Unlock()
	waitFor(t, "probe once idle", func() bool { return calls() > 0 })
}

// The benchmark, or a request, can take the engine in the middle of a round.
// The rest of the round waits until the engine is free, and probes that
// already finished are not run again.
func TestRoundStopsWhenEngineIsTaken(t *testing.T) {
	var mu sync.Mutex
	idle := true
	var ran []string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := probeName(body)
		mu.Lock()
		if !idle {
			t.Errorf("%s probe ran while the engine was taken", name)
		}
		ran = append(ran, name)
		if len(ran) == 1 {
			idle = false // the benchmark takes the engine during the first probe
		}
		mu.Unlock()
		_, _ = w.Write([]byte(passingAnswer(name)))
	}))
	t.Cleanup(engine.Close)
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
		Idle: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return idle
		},
		Reasons: true, // a reasoning model, so every check runs
	})
	ranSoFar := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ran...)
	}
	waitFor(t, "the first probe", func() bool { return len(ranSoFar()) > 0 })
	time.Sleep(50 * time.Millisecond)
	if got := ranSoFar(); len(got) != 1 {
		t.Fatalf("probes ran while the engine was taken: %v", got)
	}

	mu.Lock()
	idle = true
	mu.Unlock()
	waitFor(t, "the rest of the round", func() bool { return fullReport(platform) })
	if got := ranSoFar(); len(got) != probeCount {
		t.Fatalf("probes ran %v, want each once", got)
	}
}

// Each probe holds a slot while its request runs and gives it back after.
func TestEachProbeHoldsASlot(t *testing.T) {
	var held, claims atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n := held.Load(); n != 1 {
			t.Errorf("probe ran holding %d slots, want 1", n)
		}
		_, _ = w.Write([]byte(passingAnswer(probeName(body))))
	}))
	t.Cleanup(engine.Close)
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
		Reasons:     true, // a reasoning model, so every check runs
		Claim: func() (func(), bool) {
			claims.Add(1)
			held.Add(1)
			return func() { held.Add(-1) }, true
		},
	})
	waitFor(t, "a full report", func() bool { return fullReport(platform) })
	if n := claims.Load(); n != probeCount {
		t.Fatalf("claimed %d slots, want one per probe (%d)", n, probeCount)
	}
	if n := held.Load(); n != 0 {
		t.Fatalf("%d slots still held after the round", n)
	}
}

// A refused claim means no probe, even when the engine looks idle.
func TestNoProbeWithoutASlot(t *testing.T) {
	var granted atomic.Bool
	engine, calls := fakeEngine(t, func(int) (int, string) { return http.StatusOK, toolCallAnswer })
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "fp" },
		Idle:        func() bool { return true },
		Claim: func() (func(), bool) {
			if !granted.Load() {
				return nil, false
			}
			return func() {}, true
		},
	})
	time.Sleep(50 * time.Millisecond)
	if n := calls(); n != 0 {
		t.Fatalf("probed %d times without a slot", n)
	}
	granted.Store(true)
	waitFor(t, "probe once a slot is granted", func() bool { return calls() > 0 })
}

// noChatEngine counts every request and answers none of them with a 2xx, 400
// or 422, as the Mac mini's vLLM Metal serving baai/bge-m3 evidently did: all
// five checks stayed unverified and were retried for as long as it ran.
func noChatEngine(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { return int(calls.Load()) }
}

// notes records every status line the watcher shows.
type notes struct {
	mu    sync.Mutex
	lines []string
}

func (n *notes) note(line string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lines = append(n.lines, line)
}

func (n *notes) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.lines...)
}

// Every check is a chat request, and an embedding model answers no chat. Its
// deployment is still registered and reported, so the server drops the last
// model's results (I9), with no capability in the report (I8). The engine is
// not asked, the report is not repeated, and the console shows no
// "unverified, retrying" line.
func TestAnEmbeddingModelIsNotProbed(t *testing.T) {
	engine, calls := noChatEngine(t)
	platform := &fakePlatform{}
	lines := &notes{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "baai/bge-m3|vllm-metal|0.33.0" },
		Note:        lines.note,
		Embedding:   true,
	})
	waitFor(t, "the deployment registered and reported", func() bool {
		rep, reports, registers := platform.last()
		return registers == 1 && reports >= 1 && rep.Fingerprint == "baai/bge-m3|vllm-metal|0.33.0"
	})
	// Retry and watch waits are 5ms here: give the loop many rounds.
	time.Sleep(100 * time.Millisecond)

	if n := calls(); n != 0 {
		t.Errorf("the engine was sent %d chat checks for an embedding model", n)
	}
	rep, reports, registers := platform.last()
	if rep.Tools != nil || rep.ForcedTools != nil || rep.JSONObject != nil || rep.JSONSchema != nil || rep.Logprobs != nil || rep.Reasoning != nil {
		t.Errorf("the report claims a capability for an embedding model: %+v", rep)
	}
	if reports != 1 || registers != 1 {
		t.Errorf("reports=%d registers=%d, want the deployment reported once", reports, registers)
	}
	for _, line := range lines.all() {
		if strings.Contains(line, "unverified") || strings.Contains(line, "testing") {
			t.Errorf("the console showed %q for an embedding model", line)
			break
		}
	}
}

// The console and dashboard hide an empty Capabilities line, which left a
// provider unsure whether the checks ran. An embedding model's line says the
// checks did not run and why.
func TestAnEmbeddingModelSaysWhyItIsNotChecked(t *testing.T) {
	engine, _ := noChatEngine(t)
	platform := &fakePlatform{}
	lines := &notes{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "baai/bge-m3|llama_cpp|b4500" },
		Note:        lines.note,
		Embedding:   true,
	})
	waitFor(t, "a note", func() bool { return len(lines.all()) > 0 })
	all := lines.all()
	if got := all[len(all)-1]; got != "not run: embedding model" {
		t.Fatalf("the Capabilities line is %q, want %q", got, "not run: embedding model")
	}
}

// A chat model's line is the checks' progress, never the embedding line.
func TestAChatModelIsNotCalledAnEmbeddingModel(t *testing.T) {
	engine, _ := fakeEngine(t, nil)
	platform := &fakePlatform{}
	lines := &notes{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "Qwen/Qwen2.5-3B-Instruct|vllm-metal|0.33.0" },
		Note:        lines.note,
	})
	waitFor(t, "a full report", func() bool { return fullReport(platform) })
	for _, line := range lines.all() {
		if strings.Contains(line, "embedding") || strings.HasPrefix(line, "not run") {
			t.Fatalf("a chat model's line is %q", line)
		}
	}
}

// A chat model is still checked: all five results, which only the engine's
// answers can give, are reported. This holds before the change too.
func TestAChatModelIsStillProbed(t *testing.T) {
	engine, _ := fakeEngine(t, nil)
	platform := &fakePlatform{}
	runWatcher(t, &Watcher{
		EngineURL:   engine.URL,
		PlatformURL: platform.server(t).URL,
		Fingerprint: func() string { return "Qwen/Qwen2.5-3B-Instruct|vllm-metal|0.33.0" },
		Embedding:   false,
	})
	waitFor(t, "a full report", func() bool { return fullReport(platform) })
}
