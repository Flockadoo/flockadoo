package fakeplatform_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeplatform"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/capacity"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/logger"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/verify"
	"go.uber.org/zap"
)

// Plan item A2, section 17.3: the fake platform's behaviours. Where a client
// caller exists it is used, so the fake answers what the client reads.

func TestMain(m *testing.M) {
	logger.SetDefaultLogger(&logger.Logger{Logger: zap.NewNop()})
	os.Exit(m.Run())
}

func TestProviderRoutesNeedTheProviderKey(t *testing.T) {
	p := fakeplatform.New(t, "key")
	status, body := call(t, http.MethodPost, p.URL+"/api/provider/capacity", "wrong", `{}`)
	if status != http.StatusUnauthorized || !strings.Contains(body, `"error"`) {
		t.Fatalf("wrong key: %d %s, want 401 with the node's error body", status, body)
	}
	if status, _ := call(t, http.MethodPost, p.URL+"/api/provider/capacity", "key", `{}`); status != http.StatusNoContent {
		t.Fatalf("right key: %d, want 204", status)
	}
	if n := len(p.Requests(http.MethodPost, "/api/provider/capacity")); n != 2 {
		t.Fatalf("recorded %d capacity posts, want both", n)
	}
}

func TestTheDefaultsAnswerLikeTheNode(t *testing.T) {
	p := fakeplatform.New(t, "key")
	cases := []struct {
		method, path, body string
		status             int
		want               string
	}{
		{http.MethodPost, "/api/provider/validate_hmac", `{"hmac":"h"}`, 200, `"valid":true`},
		{http.MethodPost, "/api/provider/verify-model", `{}`, 200, `"verification_status":"verified"`},
		{http.MethodPost, "/api/provider/health", `{"object":"list","data":[]}`, 200, ``},
		{http.MethodGet, "/api/models/approved-builds?service_type=vllm", ``, 200, `"data":[]`},
		{http.MethodGet, "/api/client/version", ``, 200, `"min_supported"`},
		{http.MethodGet, "/api/provider/capabilities/deployment", ``, 200, `"revision":0`},
	}
	for _, tc := range cases {
		status, body := call(t, tc.method, p.URL+tc.path, "key", tc.body)
		if status != tc.status || !strings.Contains(body, tc.want) {
			t.Errorf("%s %s = %d %s, want %d containing %s", tc.method, tc.path, status, body, tc.status, tc.want)
		}
	}
}

func TestTheCapacitySenderReachesIt(t *testing.T) {
	p := fakeplatform.New(t, "key")
	s := capacity.NewSender(p.URL, "key", func() (int32, int32, bool) { return 1, 2, true }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Loop(ctx)
	s.Notify()
	got := p.AwaitRequests(t, http.MethodPost, "/api/provider/capacity", 1)
	if len(got) == 0 {
		t.Fatal("no capacity post recorded")
	}
	var body map[string]any
	if err := json.Unmarshal(got[0].Body, &body); err != nil || body["max_inflight"] != 2.0 {
		t.Fatalf("capacity body = %s (%v)", got[0].Body, err)
	}
}

func TestTheCatalogComesFromWhatTheTestSets(t *testing.T) {
	p := fakeplatform.New(t, "key")
	p.Set(http.MethodGet, "/api/models/approved-builds", fakeplatform.JSON(http.StatusOK, map[string]any{
		"object": "list",
		"data":   []map[string]any{{"id": "e1", "alias": "Qwen/Qwen2.5-3B-Instruct", "service_type": "vllm", "is_active": true}},
	}))
	c := verify.NewCatalog(p.URL, "vllm")
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("Qwen/Qwen2.5-3B-Instruct"); !ok {
		t.Fatalf("catalog aliases = %v", c.Aliases())
	}
}

func TestNextRepliesInOrderThenTheDefault(t *testing.T) {
	p := fakeplatform.New(t, "key")
	p.Next(http.MethodPost, "/api/provider/verify-model",
		fakeplatform.JSON(http.StatusInternalServerError, map[string]string{"error": "database down"}),
		fakeplatform.JSON(http.StatusOK, map[string]string{"verification_status": "failed"}),
	)
	want := []struct {
		status int
		body   string
	}{{500, "database down"}, {200, `"failed"`}, {200, `"verified"`}}
	for i, w := range want {
		status, body := call(t, http.MethodPost, p.URL+"/api/provider/verify-model", "key", `{}`)
		if status != w.status || !strings.Contains(body, w.body) {
			t.Fatalf("call %d = %d %s, want %d containing %s", i+1, status, body, w.status, w.body)
		}
	}
}

func TestTheNodesFailureBodies(t *testing.T) {
	p := fakeplatform.New(t, "key")
	p.Next(http.MethodPost, "/api/provider/network/node/health", fakeplatform.NodeRemoved())
	p.Next(http.MethodPost, "/api/provider/health", fakeplatform.UpgradeRequired("1.2.0", "1.3.0"))
	if status, body := call(t, http.MethodPost, p.URL+"/api/provider/network/node/health", "node-token", `{}`); status != 401 || !strings.Contains(body, "invalid node token") {
		t.Fatalf("node removed = %d %s", status, body)
	}
	status, body := call(t, http.MethodPost, p.URL+"/api/provider/health", "key", `{}`)
	if status != http.StatusUpgradeRequired || !strings.Contains(body, `"min_version":"1.2.0"`) || !strings.Contains(body, `"latest_version":"1.3.0"`) {
		t.Fatalf("upgrade required = %d %s", status, body)
	}
}

func TestHangHoldsUntilTheClientGivesUp(t *testing.T) {
	p := fakeplatform.New(t, "key")
	p.Next(http.MethodPost, "/api/provider/verify-model", fakeplatform.Hang())
	client := &http.Client{Timeout: 150 * time.Millisecond}
	req, _ := http.NewRequest(http.MethodPost, p.URL+"/api/provider/verify-model", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer key")
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a hung platform answered")
	}
	if n := len(p.Requests(http.MethodPost, "/api/provider/verify-model")); n != 1 {
		t.Fatalf("recorded %d, want 1", n)
	}
}

// The deployment register keeps state the way the node does: registering a
// new fingerprint bumps the revision and resets the attempt.
func TestTheDeploymentRegisterKeepsState(t *testing.T) {
	p := fakeplatform.New(t, "key")
	_, body := call(t, http.MethodPost, p.URL+"/api/provider/capabilities/deployment", "key", `{"fingerprint":"fp1","prev_revision":0}`)
	if !strings.Contains(body, `"revision":1`) || !strings.Contains(body, `"fingerprint":"fp1"`) {
		t.Fatalf("register = %s, want revision 1 for fp1", body)
	}
	if _, body := call(t, http.MethodGet, p.URL+"/api/provider/capabilities/deployment", "key", ``); !strings.Contains(body, `"revision":1`) || !strings.Contains(body, `"fingerprint":"fp1"`) {
		t.Fatalf("after register, GET = %s", body)
	}
	if status, _ := call(t, http.MethodPost, p.URL+"/api/provider/capabilities", "key", `{"fingerprint":"fp1","revision":1,"attempt":1}`); status != http.StatusNoContent {
		t.Fatalf("capability report = %d, want 204", status)
	}
}

// A body the client sends with a key the contract golden does not have is a
// contract error: the client and the node have drifted.
func TestAReportOutsideItsGoldenIsAContractError(t *testing.T) {
	p := fakeplatform.New(t, "key", fakeplatform.AllowContractErrors())
	call(t, http.MethodPost, p.URL+"/api/provider/health", "key", `{"object":"list","data":[],"not_in_the_golden":1}`)
	errs := p.ContractErrors()
	if len(errs) != 1 || !strings.Contains(errs[0], "not_in_the_golden") {
		t.Fatalf("contract errors = %q, want one naming not_in_the_golden", errs)
	}
	call(t, http.MethodPost, p.URL+"/api/provider/health", "key", `{"object":"list","data":[]}`)
	if errs := p.ContractErrors(); len(errs) != 1 {
		t.Fatalf("a body inside the golden added a contract error: %q", errs)
	}
}

func TestAnUnknownRouteIs404AndRecorded(t *testing.T) {
	p := fakeplatform.New(t, "key")
	if status, _ := call(t, http.MethodGet, p.URL+"/api/nothing", "key", ``); status != http.StatusNotFound {
		t.Fatalf("unknown route = %d, want 404", status)
	}
	if n := len(p.Requests(http.MethodGet, "/api/nothing")); n != 1 {
		t.Fatalf("recorded %d, want 1", n)
	}
}

func call(t *testing.T, method, url, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
