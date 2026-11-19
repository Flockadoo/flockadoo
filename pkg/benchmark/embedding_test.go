package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// embeddingEngine serves an embedding model: /v1/embeddings answers, chat does
// not. answer picks the delay and status of each embedding request from how
// many are in flight when it arrives.
type embeddingEngine struct {
	chats, embeds atomic.Int32
	inflight      atomic.Int32

	mu     sync.Mutex
	inputs []string
	models []string
}

func newEmbeddingEngine(t *testing.T, answer func(inflight int32) (time.Duration, int)) (*embeddingEngine, *httptest.Server) {
	t.Helper()
	e := &embeddingEngine{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			e.chats.Add(1)
			http.Error(w, `{"detail":"Not Found"}`, http.StatusNotFound)
			return
		}
		e.embeds.Add(1)
		n := e.inflight.Add(1)
		defer e.inflight.Add(-1)
		var body struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		e.inputs = append(e.inputs, body.Input)
		e.models = append(e.models, body.Model)
		e.mu.Unlock()
		delay, status := answer(n)
		time.Sleep(delay)
		if status != http.StatusOK {
			http.Error(w, `{"error":"busy"}`, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.011,-0.024,0.093]}],"model":"baai/bge-m3","usage":{"prompt_tokens":402,"total_tokens":402}}`))
	}))
	t.Cleanup(srv.Close)
	return e, srv
}

// measured is what a benchmark run applied and showed.
type measured struct {
	applied int
	notes   []string
}

func (m *measured) last() string {
	if len(m.notes) == 0 {
		return ""
	}
	return m.notes[len(m.notes)-1]
}

func benchWatcher(t *testing.T, srv *httptest.Server, embedding bool, configPath string) (*Watcher, *measured) {
	t.Helper()
	m := &measured{}
	return &Watcher{
		ConfigPath:  configPath,
		EngineURL:   srv.URL,
		Model:       "baai/bge-m3",
		Ready:       func() bool { return true },
		Fingerprint: func() string { return "fp" },
		client:      srv.Client(),
		Apply:       func(n int) { m.applied = n },
		Note:        func(s string) { m.notes = append(m.notes, s) },
		Embedding:   embedding,
	}, m
}

// steady answers every embedding request after d, however many are in flight.
func steady(d time.Duration) func(int32) (time.Duration, int) {
	return func(int32) (time.Duration, int) { return d, http.StatusOK }
}

// An embedding model answers no chat, so the benchmark measured nothing and
// fell back to the default while saying "verified". It is measured with
// embedding requests of about 300 words: an engine whose throughput keeps
// rising passes every level.
func TestAnEmbeddingModelIsMeasuredWithEmbeddingRequests(t *testing.T) {
	e, srv := newEmbeddingEngine(t, steady(50*time.Millisecond))
	w, m := benchWatcher(t, srv, true, filepath.Join(t.TempDir(), "config.yaml"))
	w.measure(context.Background(), "fp")

	if n := e.chats.Load(); n != 0 {
		t.Errorf("the benchmark sent %d chat requests to an embedding model", n)
	}
	if n := e.embeds.Load(); n != 1+2+4+8+16 {
		t.Errorf("embedding requests = %d, want one per stream at 1, 2, 4, 8 and 16", n)
	}
	if m.applied != 16 {
		t.Errorf("applied concurrency = %d, want 16", m.applied)
	}
	if got := m.last(); got != "verified for 16 concurrent" {
		t.Errorf("note = %q, want %q", got, "verified for 16 concurrent")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, in := range e.inputs {
		if words := len(strings.Fields(in)); words < 250 || words > 400 {
			t.Errorf("input %d has %d words, want about 300 (it must fit a 512-token model)", i, words)
			break
		}
		if e.models[i] != "baai/bge-m3" {
			t.Errorf("request %d asked for model %q", i, e.models[i])
			break
		}
	}
}

// Total throughput must still rise by 10% for a level to pass, as for chat.
func TestAnEmbeddingLevelThatLosesThroughputStopsTheClimb(t *testing.T) {
	e, srv := newEmbeddingEngine(t, func(inflight int32) (time.Duration, int) {
		if inflight == 1 {
			return 50 * time.Millisecond, http.StatusOK
		}
		return 150 * time.Millisecond, http.StatusOK
	})
	w, m := benchWatcher(t, srv, true, filepath.Join(t.TempDir(), "config.yaml"))
	w.measure(context.Background(), "fp")

	if m.applied != 1 {
		t.Errorf("applied concurrency = %d, want 1 (2 concurrent gave less throughput)", m.applied)
	}
	if n := e.embeds.Load(); n != 1+2 {
		t.Errorf("embedding requests = %d, want the climb to stop after 2", n)
	}
}

// A level with any failed request does not pass.
func TestAnEmbeddingLevelWithErrorsStopsTheClimb(t *testing.T) {
	_, srv := newEmbeddingEngine(t, func(inflight int32) (time.Duration, int) {
		if inflight > 2 {
			return 50 * time.Millisecond, http.StatusServiceUnavailable
		}
		return 50 * time.Millisecond, http.StatusOK
	})
	w, m := benchWatcher(t, srv, true, filepath.Join(t.TempDir(), "config.yaml"))
	w.measure(context.Background(), "fp")

	if m.applied != 2 {
		t.Errorf("applied concurrency = %d, want 2 (4 concurrent had errors)", m.applied)
	}
}

// A level whose slowest request takes over 3 seconds does not pass.
func TestAnEmbeddingLevelSlowerThanTheLimitStopsTheClimb(t *testing.T) {
	_, srv := newEmbeddingEngine(t, func(inflight int32) (time.Duration, int) {
		if inflight > 2 {
			return 3100 * time.Millisecond, http.StatusOK
		}
		return 50 * time.Millisecond, http.StatusOK
	})
	w, m := benchWatcher(t, srv, true, filepath.Join(t.TempDir(), "config.yaml"))
	w.measure(context.Background(), "fp")

	if m.applied != 2 {
		t.Errorf("applied concurrency = %d, want 2 (a request at 4 concurrent took over 3s)", m.applied)
	}
}

// When no level runs at all, the fallback is applied and shown as not
// measured, for chat and embedding models alike, and again when it is read
// back from the cache.
func TestAFailedBenchmarkIsNotCalledVerified(t *testing.T) {
	for _, embedding := range []bool{true, false} {
		t.Run(fmt.Sprintf("embedding=%v", embedding), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "fail", http.StatusInternalServerError)
			}))
			t.Cleanup(srv.Close)
			config := filepath.Join(t.TempDir(), "config.yaml")

			w, m := benchWatcher(t, srv, embedding, config)
			w.measure(context.Background(), "fp")
			if m.applied != 4 {
				t.Fatalf("applied concurrency = %d, want the fallback 4", m.applied)
			}
			if got := m.last(); strings.Contains(got, "verified") || !strings.Contains(got, "not measured") {
				t.Errorf("note = %q, want it to say the 4 was not measured", got)
			}

			cached, mc := benchWatcher(t, srv, embedding, config)
			cached.step(context.Background())
			if got := mc.last(); strings.Contains(got, "verified") || !strings.Contains(got, "not measured") {
				t.Errorf("note from the cache = %q, want it to say the 4 was not measured", got)
			}
		})
	}
}

// A chat model is still measured with chat, and no embedding request is sent.
// This holds before the change too.
func TestAChatModelIsStillMeasuredWithChat(t *testing.T) {
	var chats, embeds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/embeddings" {
			embeds.Add(1)
			http.Error(w, "not an embedding model", http.StatusBadRequest)
			return
		}
		chats.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 4; i++ {
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	w, _ := benchWatcher(t, srv, false, filepath.Join(t.TempDir(), "config.yaml"))
	w.measure(context.Background(), "fp")

	if embeds.Load() != 0 {
		t.Errorf("a chat model was sent %d embedding requests", embeds.Load())
	}
	if chats.Load() == 0 {
		t.Error("a chat model was not measured with chat")
	}
}
