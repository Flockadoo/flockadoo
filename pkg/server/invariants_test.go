package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakegateway"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeplatform"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/capacity"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/connect"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/health"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Plan item A3: the invariants of section 13, the lifecycle rows of 6.4 and
// the properties of 7.3 that hold today, through the harness: the real tunnel
// client and the real relay between the fake gateway, platform and engine.
// Each test is named in FEATURES.yaml's invariants checklist.

const (
	rigKey   = "sk-test-SECRET-0123456789abcdef"
	rigModel = "fake/model"
)

type rig struct {
	s    *Server
	eng  *fakeengine.Engine
	plat *fakeplatform.Platform
	gw   *fakegateway.Gateway
}

// newRig starts the relay with limit slots behind a live tunnel.
func newRig(t *testing.T, limit int32) *rig {
	t.Helper()
	r := &rig{
		eng:  fakeengine.VLLM(t, fakeengine.Model(rigModel)),
		plat: fakeplatform.New(t, rigKey),
		gw:   fakegateway.New(t, rigKey),
	}
	cfg := &config.Config{}
	cfg.Provider.URL = r.plat.URL
	cfg.Provider.APIKey = rigKey
	cfg.Provider.ProviderType = "vllm"
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = rigModel
	r.s = &Server{config: cfg, llmClient: llm.NewClient("vllm", r.eng.URL, 30*time.Second), sessionQueueWait: 5 * time.Second}
	r.s.maxInflight.Store(limit)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = connect.New(r.gw.URL, rigKey, r.s.inferenceMux(), nil).Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	r.gw.AwaitSession(t)
	return r
}

// send sends a chat request through the gateway as the node does. A header
// set to "" in hdr is removed.
func (r *rig) send(ctx context.Context, body string, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "hmac-ok")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	return r.gw.Do(req)
}

func (r *rig) call(t *testing.T, body string, hdr map[string]string) (int, string) {
	t.Helper()
	resp, err := r.send(context.Background(), body, hdr)
	if err != nil {
		t.Fatalf("request through the gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// awaitSlotsFree waits until every slot has been returned. The slot comes
// back when the handler returns, a moment after the gateway sees the end.
func (r *rig) awaitSlotsFree(t *testing.T, scenario string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.s.inflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d slots still taken", scenario, r.s.inflight.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const streamBody = `{"model":"fake/model","stream":true}`

// I1, 7.3 (limit): however requests arrive, and after the limit is lowered,
// the engine never runs more than the limit at once. Half of them are turns
// of one conversation, which queue for a slot instead of being refused.
func TestTheEngineNeverRunsMoreThanTheLimit(t *testing.T) {
	r := newRig(t, 2)
	burst := func(n int, session string) (served int) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range n {
			r.eng.Next(fakeengine.Stream("a", "b", "c").Slow(15 * time.Millisecond))
			hdr := map[string]string{}
			if i%2 == 0 {
				hdr["X-Session-Key"] = session
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if status, _ := r.call(t, streamBody, hdr); status == http.StatusOK {
					mu.Lock()
					served++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		return served
	}

	if served := burst(12, "conversation-1"); served < 2 {
		t.Fatalf("only %d of 12 requests were served; the test did not load the engine", served)
	}
	if got := r.eng.MaxInFlight(); got > 2 {
		t.Fatalf("the engine ran %d requests at once with a limit of 2", got)
	}
	r.awaitSlotsFree(t, "after the first burst")

	r.s.SetMaxInflight(1)
	r.eng.ResetMaxInFlight()
	if served := burst(8, "conversation-2"); served < 1 {
		t.Fatal("nothing was served after the limit was lowered")
	}
	if got := r.eng.MaxInFlight(); got > 1 {
		t.Fatalf("the engine ran %d requests at once after the limit was lowered to 1", got)
	}
	r.awaitSlotsFree(t, "after the second burst")
}

// I3, 7.3 (rejected requests do not execute): every refusal the relay makes
// today happens before the engine is called.
func TestRefusedRequestsNeverReachTheEngine(t *testing.T) {
	r := newRig(t, 1)
	cases := []struct {
		name   string
		setup  func() (undo func())
		body   string
		hdr    map[string]string
		status int
	}{
		{"no request id", nil, streamBody, map[string]string{"X-Request-ID": ""}, http.StatusUnauthorized},
		{"a request id the node does not know", func() func() {
			r.plat.Next(http.MethodPost, "/api/provider/validate_hmac", fakeplatform.JSON(http.StatusOK, map[string]bool{"valid": false}))
			return func() {}
		}, streamBody, nil, http.StatusUnauthorized},
		{"a model this machine does not serve", nil, `{"model":"other/model","stream":true}`, nil, http.StatusServiceUnavailable},
		{"the gate is closed for a benchmark", func() func() {
			r.s.SetBenchmarking(true)
			return func() { r.s.SetBenchmarking(false) }
		}, streamBody, nil, http.StatusServiceUnavailable},
		{"every slot is taken", func() func() {
			r.eng.Next(fakeengine.Stream("x").Stall(1))
			ctx, cancel := context.WithCancel(context.Background())
			resp, err := r.send(ctx, streamBody, nil) //nolint:bodyclose // the returned undo closes it once the refusal has been checked
			if err != nil {
				t.Fatal(err)
			}
			r.eng.AwaitInFlight(t, 1)
			return func() {
				cancel()
				_ = resp.Body.Close()
				r.eng.AwaitIdle(t)
				r.awaitSlotsFree(t, "after the slot holder left")
			}
		}, streamBody, nil, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			undo := func() {}
			if tc.setup != nil {
				undo = tc.setup()
			}
			before := len(r.eng.Requests())
			status, body := r.call(t, tc.body, tc.hdr)
			after := len(r.eng.Requests())
			undo()
			if status != tc.status {
				t.Fatalf("status %d (%s), want %d", status, body, tc.status)
			}
			if after != before {
				t.Fatalf("a refused request reached the engine (%d requests before, %d after)", before, after)
			}
		})
	}
	r.awaitSlotsFree(t, "after the refusals")
}

// I2, 7.3 (released once, failures do not leak): whatever happens to an
// admitted request, its slot comes back.
func TestEverySlotComesBack(t *testing.T) {
	r := newRig(t, 1)
	scenarios := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"a completion", func(t *testing.T) {
			r.eng.Next(fakeengine.Complete("hi", fakeengine.Usage{PromptTokens: 1, CompletionTokens: 1}))
			r.call(t, `{"model":"fake/model"}`, nil)
		}},
		{"a stream", func(t *testing.T) {
			r.eng.Next(fakeengine.Stream("a", "b"))
			r.call(t, streamBody, nil)
		}},
		{"an engine error", func(t *testing.T) {
			r.eng.Next(fakeengine.Status(http.StatusInternalServerError, `{"error":"oom"}`))
			r.call(t, streamBody, nil)
		}},
		{"a body that is not JSON", func(t *testing.T) {
			r.eng.Next(fakeengine.InvalidJSON())
			r.call(t, `{"model":"fake/model"}`, nil)
		}},
		{"an engine that drops the stream", func(t *testing.T) {
			r.eng.Next(fakeengine.Stream("a", "b", "c").Partial(1))
			r.call(t, streamBody, nil)
		}},
		{"a client that gives up on a hung engine", func(t *testing.T) {
			r.eng.Next(fakeengine.Hang())
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if resp, err := r.send(ctx, streamBody, nil); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			r.eng.AwaitIdle(t)
		}},
		{"an engine that crashes", func(t *testing.T) {
			r.eng.Next(fakeengine.Crash())
			r.call(t, streamBody, nil)
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			sc.run(t)
			r.awaitSlotsFree(t, sc.name)
		})
	}
}

// readFirstLine reads the first line of a streamed answer, so the test knows
// the stream is open end to end.
func readFirstLine(t *testing.T, resp *http.Response) {
	t.Helper()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first line %q, %v", line, err)
	}
}

// I4, 7.3 (cancellation): when the gateway gives up on a stream, the engine
// call is cancelled and the slot comes back.
func TestACancelledStreamReleasesTheEngineAndTheSlot(t *testing.T) {
	r := newRig(t, 1)
	r.eng.Next(fakeengine.Stream("a", "b").Stall(1))
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := r.send(ctx, streamBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	readFirstLine(t, resp)
	cancel()
	_ = resp.Body.Close()
	r.eng.AwaitIdle(t)
	if reqs := r.eng.Requests(); len(reqs) != 1 || !reqs[0].Cancelled {
		t.Fatalf("engine requests = %+v, want one, cancelled", reqs)
	}
	r.awaitSlotsFree(t, "after the gateway gave up")
}

// 6.4 (in flight when the session drops): the tunnel goes away under a
// stream; its engine call is cancelled and its slot comes back.
func TestARequestInFlightWhenTheSessionDropsIsCancelled(t *testing.T) {
	r := newRig(t, 1)
	r.eng.Next(fakeengine.Stream("a", "b").Stall(1))
	resp, err := r.send(context.Background(), streamBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	readFirstLine(t, resp)
	r.gw.Drop()
	r.eng.AwaitIdle(t)
	if reqs := r.eng.Requests(); len(reqs) != 1 || !reqs[0].Cancelled {
		t.Fatalf("engine requests = %+v, want one, cancelled", reqs)
	}
	r.awaitSlotsFree(t, "after the session dropped")
}

// I7 (the benchmark part): while a benchmark has the engine, the next
// capacity report says not accepting, and the one after it says accepting.
func TestABenchmarkClosesTheGateInTheNextCapacityReport(t *testing.T) {
	r := newRig(t, 2)
	sender := capacity.NewSender(r.plat.URL, rigKey, r.s.SlotSnapshot, nil)
	r.s.SetCapacityNotify(sender.Notify)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sender.Loop(ctx)

	accepting := func(n int) bool {
		posts := r.plat.AwaitRequests(t, http.MethodPost, "/api/provider/capacity", n)
		return strings.Contains(string(posts[n-1].Body), `"accepting":true`)
	}
	r.s.SetBenchmarking(true)
	if accepting(1) {
		t.Fatal("the report sent when the benchmark started says accepting")
	}
	r.s.SetBenchmarking(false)
	if !accepting(2) {
		t.Fatal("the report sent when the benchmark ended says not accepting")
	}
}

// I12: the API key goes to the node only in the Authorization header. It is
// in no log line, no status page, no request body and no other header,
// through a served request, a refused one, a capacity report and a health
// report.
func TestTheKeyAppearsOnlyInTheAuthorizationHeader(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger.SetDefaultLogger(&logger.Logger{Logger: zap.New(core)})
	t.Cleanup(func() { logger.SetDefaultLogger(&logger.Logger{Logger: zap.NewNop()}) })

	r := newRig(t, 1)
	r.call(t, streamBody, nil)
	r.plat.Next(http.MethodPost, "/api/provider/validate_hmac", fakeplatform.JSON(http.StatusUnauthorized, map[string]string{"error": "unknown request"}))
	r.call(t, streamBody, nil)

	sender := capacity.NewSender(r.plat.URL, rigKey, r.s.SlotSnapshot, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sender.Loop(ctx)
	sender.Notify()
	r.plat.AwaitRequests(t, http.MethodPost, "/api/provider/capacity", 1)

	if err := health.NewReporter(r.s.config, nil, r.s.llmClient).SendHealthReport(context.Background()); err != nil {
		t.Fatalf("health report: %v", err)
	}
	status := httptest.NewRecorder()
	r.s.handleStatus(status, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	var seen strings.Builder
	for _, e := range logs.All() {
		_, _ = fmt.Fprintf(&seen, "%s %v\n", e.Message, e.ContextMap())
	}
	seen.WriteString(status.Body.String())
	authorized := 0
	for _, req := range r.plat.AllRequests() {
		seen.Write(req.Body)
		seen.WriteString(req.RawQuery)
		for name, values := range req.Header {
			if name == "Authorization" {
				if values[0] == "Bearer "+rigKey {
					authorized++
				}
				continue
			}
			seen.WriteString(strings.Join(values, " "))
		}
	}
	for _, d := range r.gw.Dials() {
		seen.WriteString(d.ClientVersion)
	}
	if strings.Contains(seen.String(), rigKey) {
		t.Fatal("the API key appears outside the Authorization header")
	}
	if authorized == 0 {
		t.Fatal("no request to the node carried the key; the test did not exercise it")
	}
}
