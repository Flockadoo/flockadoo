// Package fakeplatform is the server's HTTP API for tests (plan section 17.3):
// one handler per endpoint the client calls, each recording the request and
// answering from a script, with defaults that answer the way the server does.
//
// Provider routes need the provider key, as on the server. A request body sent
// to an endpoint that has a contract golden (pkg/contract/testdata) may only
// use keys the golden has; anything else is a contract error, and a test with
// a contract error fails when it ends.
package fakeplatform

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Platform is a running fake node. It stops when the test ends.
type Platform struct {
	URL string

	apiKey        string
	allowContract bool

	mu             sync.Mutex
	defaults       map[route]Reply
	queued         map[route][]Reply
	requests       []Request
	contractErrors []string
	deployment     deployment
	changed        chan struct{}
}

type route struct{ method, path string }

type deployment struct {
	Fingerprint string `json:"fingerprint"`
	Revision    int64  `json:"revision"`
	Attempt     int64  `json:"attempt"`
}

// Option configures a Platform.
type Option func(*Platform)

// AllowContractErrors keeps contract errors from failing the test, for a
// test that checks they are found.
func AllowContractErrors() Option { return func(p *Platform) { p.allowContract = true } }

// goldens maps an endpoint to the contract golden its request body must fit.
var goldens = map[route]string{
	{http.MethodPost, "/api/provider/health"}:       "health.request.json",
	{http.MethodPost, "/api/provider/verify-model"}: "verify-model.request.json",
}

// New starts a fake node that accepts apiKey on provider routes.
func New(t testing.TB, apiKey string, opts ...Option) *Platform {
	t.Helper()
	p := &Platform{
		apiKey:   apiKey,
		defaults: defaultReplies(),
		queued:   map[route][]Reply{},
		changed:  make(chan struct{}),
	}
	for _, o := range opts {
		o(p)
	}
	srv := httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	p.URL = srv.URL
	t.Cleanup(func() {
		srv.CloseClientConnections() // a Hang whose client never left must not hang the test
		srv.Close()
		if errs := p.ContractErrors(); len(errs) > 0 && !p.allowContract {
			t.Errorf("fakeplatform: the client sent bodies outside their contract golden:\n%s", strings.Join(errs, "\n"))
		}
	})
	return p
}

// Reply is a scripted answer. Build it with JSON, NoContent, Hang,
// NodeRemoved or UpgradeRequired.
type Reply struct {
	status int
	body   []byte
	hang   bool
	delay  time.Duration
	state  func(p *Platform, r Request) (int, []byte) // stateful defaults; called with p.mu held
}

// JSON answers status with v as its JSON body.
func JSON(status int, v any) Reply {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("fakeplatform.JSON: %v", err))
	}
	return Reply{status: status, body: b}
}

// NoContent answers 204.
func NoContent() Reply { return Reply{status: http.StatusNoContent} }

// Hang accepts the request and never answers; the client's timeout decides.
func Hang() Reply { return Reply{hang: true} }

// Delayed holds the request for d, then answers r: a server that is slow but
// answers. A client that gives up first gets nothing.
func Delayed(d time.Duration, r Reply) Reply {
	r.delay = d
	return r
}

// NodeRemoved is the server's answer to a flock machine whose node token was
// revoked because the owner removed it.
func NodeRemoved() Reply {
	return JSON(http.StatusUnauthorized, map[string]string{"error": "invalid node token"})
}

// UpgradeRequired is the server's 426 for a client below the minimum version.
func UpgradeRequired(minVersion, latest string) Reply {
	return JSON(http.StatusUpgradeRequired, map[string]string{
		"error": "client upgrade required", "min_version": minVersion, "latest_version": latest, "download_url": "",
	})
}

func defaultReplies() map[route]Reply {
	return map[route]Reply{
		{http.MethodPost, "/api/provider/validate_hmac"}: JSON(http.StatusOK, map[string]bool{"valid": true}),
		{http.MethodPost, "/api/provider/verify-model"}:  JSON(http.StatusOK, map[string]string{"verification_status": "verified"}),
		{http.MethodPost, "/api/provider/health"}:        JSON(http.StatusOK, map[string]string{"status": "ok"}),
		{http.MethodPost, "/api/provider/capacity"}:      NoContent(),
		{http.MethodGet, "/api/models/approved-builds"}:  JSON(http.StatusOK, map[string]any{"object": "list", "data": []any{}}),
		{http.MethodGet, "/api/client/version"}:          JSON(http.StatusOK, map[string]string{"latest": "0.0.0", "min_supported": "0.0.0", "download_url": ""}),
		{http.MethodGet, "/api/provider/capabilities/deployment"}: {state: func(p *Platform, _ Request) (int, []byte) {
			b, _ := json.Marshal(p.deployment)
			return http.StatusOK, b
		}},
		{http.MethodPost, "/api/provider/capabilities/deployment"}: {state: func(p *Platform, r Request) (int, []byte) {
			var in struct {
				Fingerprint string `json:"fingerprint"`
			}
			if json.Unmarshal(r.Body, &in) != nil || in.Fingerprint == "" {
				return http.StatusBadRequest, []byte(`{"error":"fingerprint is required"}`)
			}
			p.deployment = deployment{Fingerprint: in.Fingerprint, Revision: p.deployment.Revision + 1}
			b, _ := json.Marshal(p.deployment)
			return http.StatusOK, b
		}},
		{http.MethodPost, "/api/provider/capabilities"}: {state: func(p *Platform, r Request) (int, []byte) {
			var in deployment
			if json.Unmarshal(r.Body, &in) != nil || in.Revision != p.deployment.Revision || in.Attempt <= p.deployment.Attempt {
				return http.StatusConflict, []byte(`{"error":"stale capability report"}`)
			}
			p.deployment.Attempt = in.Attempt
			return http.StatusNoContent, nil
		}},
	}
}

// Set makes reply the default answer for method and path.
func (p *Platform) Set(method, path string, reply Reply) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.defaults[route{method, path}] = reply
}

// Next queues answers for the next requests to method and path; after them
// the default answers again.
func (p *Platform) Next(method, path string, replies ...Reply) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := route{method, path}
	p.queued[k] = append(p.queued[k], replies...)
}

// Request is one request the platform received.
type Request struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Body                   []byte
	At                     time.Time
}

// Requests returns every request to method and path so far, in order.
func (p *Platform) Requests(method, path string) []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Request
	for _, r := range p.requests {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// AllRequests returns every request the platform received, in order.
func (p *Platform) AllRequests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

// AwaitRequests waits until n requests to method and path have arrived and
// returns them.
func (p *Platform) AwaitRequests(t testing.TB, method, path string, n int) []Request {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		got := p.Requests(method, path)
		if len(got) >= n {
			return got
		}
		p.mu.Lock()
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("fakeplatform: timed out waiting for %d %s %s (got %d)", n, method, path, len(got))
			return nil
		}
	}
}

// ContractErrors lists every body the client sent outside its golden.
func (p *Platform) ContractErrors() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.contractErrors...)
}

func (p *Platform) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := Request{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: body, At: time.Now()}
	k := route{r.Method, r.URL.Path}

	p.mu.Lock()
	p.requests = append(p.requests, req)
	close(p.changed)
	p.changed = make(chan struct{})
	if golden, ok := goldens[k]; ok {
		if problem := outsideGolden(golden, body); problem != "" {
			p.contractErrors = append(p.contractErrors, fmt.Sprintf("%s %s: %s", r.Method, r.URL.Path, problem))
		}
	}
	if needsProviderKey(r.URL.Path) && r.Header.Get("Authorization") != "Bearer "+p.apiKey {
		p.mu.Unlock()
		writeBody(w, http.StatusUnauthorized, []byte(`{"error":"invalid API key"}`))
		return
	}
	reply, ok := p.defaults[k]
	if q := p.queued[k]; len(q) > 0 {
		reply, ok = q[0], true
		p.queued[k] = q[1:]
	}
	status, out := http.StatusNotFound, []byte(`{"error":"not found"}`)
	if ok && reply.state != nil {
		status, out = reply.state(p, req)
	} else if ok {
		status, out = reply.status, reply.body
	}
	p.mu.Unlock()

	if ok && reply.hang {
		<-r.Context().Done()
		return
	}
	if ok && reply.delay > 0 {
		select {
		case <-time.After(reply.delay):
		case <-r.Context().Done():
			return
		}
	}
	writeBody(w, status, out)
}

// needsProviderKey: the provider routes take the provider key; a flock
// machine's node routes take its node token instead.
func needsProviderKey(path string) bool {
	return strings.HasPrefix(path, "/api/provider/") && !strings.HasPrefix(path, "/api/provider/network/node/")
}

func writeBody(w http.ResponseWriter, status int, body []byte) {
	if len(body) > 0 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// outsideGolden returns a description of every top-level key in body that
// the golden does not have, or "" if there is none.
func outsideGolden(golden string, body []byte) string {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "pkg", "contract", "testdata", golden))
	if err != nil {
		return "cannot read golden " + golden + ": " + err.Error()
	}
	var want, got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		return "golden " + golden + " is not a JSON object"
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return "body is not a JSON object"
	}
	var extra []string
	for k := range got {
		if _, ok := want[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(extra) == 0 {
		return ""
	}
	sort.Strings(extra)
	return fmt.Sprintf("keys not in %s: %s", golden, strings.Join(extra, ", "))
}
