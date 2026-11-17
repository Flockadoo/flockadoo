package capacity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
)

// Characterization tests (plan item G1): the capacity report as the client
// sends it today. They pass before and after any change that must keep it.

type capturedPost struct {
	method, path, auth, contentType string
	body                            map[string]any
}

func capacityNode(t *testing.T) (*httptest.Server, <-chan capturedPost) {
	t.Helper()
	posts := make(chan capturedPost, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("capacity body is not JSON: %v: %s", err, raw)
		}
		posts <- capturedPost{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, posts
}

func nextPost(t *testing.T, posts <-chan capturedPost) capturedPost {
	t.Helper()
	select {
	case p := <-posts:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no capacity report reached the node")
		return capturedPost{}
	}
}

func TestNotifySendsTheSlotsToTheNode(t *testing.T) {
	srv, posts := capacityNode(t)
	slots := func() (int32, int32, bool) { return 2, 4, true }
	s := NewSender(srv.URL+"/", "test-key", slots, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Loop(ctx)

	s.Notify()
	first := nextPost(t, posts)
	if first.method != http.MethodPost || first.path != "/api/provider/capacity" {
		t.Fatalf("sent %s %s, want POST /api/provider/capacity", first.method, first.path)
	}
	if first.auth != "Bearer test-key" || first.contentType != "application/json" {
		t.Fatalf("headers: Authorization %q, Content-Type %q", first.auth, first.contentType)
	}
	if first.body["inflight"] != 2.0 || first.body["max_inflight"] != 4.0 || first.body["accepting"] != true {
		t.Fatalf("body = %v, want inflight 2, max_inflight 4, accepting true", first.body)
	}
	if first.body["seq"] != 1.0 || first.body["instance"] == "" {
		t.Fatalf("body = %v, want seq 1 and an instance id", first.body)
	}
	if _, ok := first.body["engine_waiting"]; ok {
		t.Fatalf("an engine with no metrics still sent engine_waiting: %v", first.body)
	}

	s.Notify()
	second := nextPost(t, posts)
	if second.body["seq"] != 2.0 || second.body["instance"] != first.body["instance"] {
		t.Fatalf("second report = %v, want seq 2 and the same instance as %v", second.body, first.body["instance"])
	}
}

// metricsEngine is an engine that reports its queue and KV cache.
type metricsEngine struct{ llm.Client }

func (metricsEngine) EngineCapacity(context.Context) (llm.EngineCapacity, error) {
	waiting, usage := int32(3), 0.5
	return llm.EngineCapacity{Waiting: &waiting, KVCacheUsage: &usage}, nil
}

func TestTheEnginesQueueAndCacheGoWithTheReport(t *testing.T) {
	srv, posts := capacityNode(t)
	slots := func() (int32, int32, bool) { return 0, 1, false }
	s := NewSender(srv.URL, "k", slots, metricsEngine{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Loop(ctx)

	s.Notify()
	p := nextPost(t, posts)
	if p.body["engine_waiting"] != 3.0 || p.body["kv_cache_usage"] != 0.5 || p.body["accepting"] != false {
		t.Fatalf("body = %v, want engine_waiting 3, kv_cache_usage 0.5, accepting false", p.body)
	}
}
