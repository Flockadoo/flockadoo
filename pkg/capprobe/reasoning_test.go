package capprobe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The thinking of a reasoning model, as each engine sends it when it was
// started with a reasoning parser, and as vLLM sends it without one.
const (
	vllmThinking  = `{"choices":[{"message":{"content":"42","reasoning":"17 plus 25 is 42."}}]}`
	llamaThinking = `{"choices":[{"message":{"content":"42","reasoning_content":"17 plus 25 is 42."}}]}`
	inlineThinker = `{"choices":[{"message":{"content":"<think>\n17 plus 25 is 42.\n</think>\n\n42"}}]}`
)

// The probe passes only when the thinking comes back apart from the answer:
// the inference node routes a reasoning model only to providers that do.
func TestReasoningProbePassesOnlyWhenThinkingComesApart(t *testing.T) {
	for answer, want := range map[string]bool{
		vllmThinking:  true,
		llamaThinking: true,
		inlineThinker: false,
		`{"choices":[{"message":{"content":"42","reasoning":"  "}}]}`: false,
		`{"choices":[{"message":{"content":"42"}}]}`:                  false,
	} {
		if got := probePassed("reasoning", []byte(answer)); got != want {
			t.Errorf("probePassed(%s) = %v, want %v", answer, got, want)
		}
	}
}

// The probe asks the model to think: Gemma 4 thinks only when asked, and
// vLLM turns enable_thinking on for any reasoning_effort but none.
func TestReasoningProbeAsksToThink(t *testing.T) {
	body := probeBody("m", "reasoning")
	if effort, _ := body["reasoning_effort"].(string); effort == "" || effort == "none" {
		t.Fatalf("reasoning_effort = %v", body["reasoning_effort"])
	}
	if n, _ := body["max_tokens"].(int); n < 1024 {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
}

// reasoningEngine passes every probe and counts the reasoning ones.
func reasoningEngine(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := probeName(body)
		if name == "reasoning" {
			asked.Add(1)
		}
		_, _ = w.Write([]byte(passingAnswer(name)))
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func TestAReasoningModelIsProbedAndReported(t *testing.T) {
	engine, asked := reasoningEngine(t)
	platform := &fakePlatform{}
	ps := platform.server(t)
	runWatcher(t, &Watcher{EngineURL: engine.URL, PlatformURL: ps.URL, Reasons: true,
		Fingerprint: func() string { return "fp" }})
	waitFor(t, "a report with reasoning", func() bool {
		rep, _, _ := platform.last()
		return fullReport(platform) && rep.Reasoning != nil
	})
	if rep, _, _ := platform.last(); !*rep.Reasoning || asked.Load() == 0 {
		t.Fatalf("reasoning = %v after %d probes", *rep.Reasoning, asked.Load())
	}
}

// A model that does not reason is never asked to think, and its report
// leaves the field out instead of claiming false.
func TestAModelThatDoesNotReasonIsNotProbedForIt(t *testing.T) {
	engine, asked := reasoningEngine(t)
	platform := &fakePlatform{}
	ps := platform.server(t)
	runWatcher(t, &Watcher{EngineURL: engine.URL, PlatformURL: ps.URL,
		Fingerprint: func() string { return "fp" }})
	waitFor(t, "a full report", func() bool { return fullReport(platform) })
	if rep, _, _ := platform.last(); rep.Reasoning != nil || asked.Load() != 0 {
		t.Fatalf("reasoning = %v after %d probes, want no probe and no field", rep.Reasoning, asked.Load())
	}
}

func TestLabelLeavesOutACheckThatDoesNotApply(t *testing.T) {
	outcomes := []Outcome{Supported, Supported, Supported, Supported, Supported, NotApplicable}
	if got := Label(outcomes); got != "completed" {
		t.Fatalf("Label = %q", got)
	}
	outcomes[0] = Waiting
	if got := Label(outcomes); got != "testing - 4/5 checked (still serving requests)" {
		t.Fatalf("Label = %q", got)
	}
}
