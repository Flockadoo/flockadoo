package benchmark

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
)

// replay is one benchmark stream against a recording of a real engine, and
// the number of generated tokens the benchmark counted.
func replay(t *testing.T, recording string) int {
	t.Helper()
	e := fakeengine.VLLM(t)
	e.Next(fakeengine.Replay(t, recording))
	got := streamOnce(context.Background(), &http.Client{}, e.URL, "m")
	if got.err != nil {
		t.Fatalf("%s: %v", recording, got.err)
	}
	return got.tokens
}

// A reasoning model streams its thinking apart from its answer: vLLM as
// delta.reasoning, llama.cpp as delta.reasoning_content. Each chunk is a
// generated token. Counting only content timed the thinking with almost no
// tokens: a GB10 decoding 13 tok/s read as 4.5, an H100 as 1.
func TestStreamOnceCountsThinking(t *testing.T) {
	cases := []struct {
		recording string
		want      int
	}{
		{"vllm-0.28.0-qwen3-1.7b-reasoning/chat-plain", 63},       // all thinking, delta.reasoning
		{"llamacpp-b12359-qwen3-1.7b-reasoning/chat-plain", 62},   // all thinking, delta.reasoning_content
		{"vllm-0.28.0-qwen3-1.7b-reasoning/chat-long", 276 + 122}, // thinking, then the answer
	}
	for _, tc := range cases {
		t.Run(tc.recording, func(t *testing.T) {
			if got := replay(t, tc.recording); got != tc.want {
				t.Errorf("counted %d tokens, want %d", got, tc.want)
			}
		})
	}
}

// Only chunks that carry generated text count. The empty role chunk both
// engines send first is not a token.
func TestStreamOnceCountsOnlyGeneratedText(t *testing.T) {
	for _, rec := range []string{
		"vllm-0.28.0-qwen2.5-3b-instruct/chat-plain",
		"llamacpp-b12359-qwen2.5-3b-instruct/chat-plain",
	} {
		t.Run(rec, func(t *testing.T) {
			if got := replay(t, rec); got != 5 {
				t.Errorf("counted %d tokens, want the 5 chunks with text", got)
			}
		})
	}
}

// A result saved before the benchmark counted thinking is wrong for every
// reasoning model, and a restart reuses the cache for the same deployment. It
// is measured again once. A result saved by the current count is reused.
func TestAResultCachedByTheOldCountIsMeasuredAgain(t *testing.T) {
	var chats atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chats.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 4; i++ {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	config := filepath.Join(t.TempDir(), "config.yaml")
	// What PeiPei saved on 2026-11-19: no count method, thinking not counted.
	old := `{"fingerprint":"fp","concurrency":1,"measured_at":"2026-11-19T08:13:47Z",` +
		`"levels":[{"n":1,"errors":0,"preemptions":0,"p95_ttft":412664296,"min_decode_tps":4.49,"aggregate_tps":4.45}]}`
	if err := os.WriteFile(pathFor(config), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	w, _ := benchWatcher(t, srv, false, config)
	w.Model = "m"
	w.step(context.Background())
	measured := chats.Load()
	if measured == 0 {
		t.Fatal("the old cached result was reused; want the deployment measured again")
	}

	again, _ := benchWatcher(t, srv, false, config)
	again.Model = "m"
	again.step(context.Background())
	if n := chats.Load(); n != measured {
		t.Errorf("a result saved by the current count was measured again (%d more requests)", n-measured)
	}
}
