package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakegateway"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeplatform"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/connect"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
)

// Plan item A2's acceptance: a recorded stream goes from the fake gateway
// through the real tunnel client and the real relay to the fake engine, and
// comes back to the gateway byte for byte.
func TestARecordedStreamCrossesTheRealTunnelByteForByte(t *testing.T) {
	const rec = "vllm-0.28.0-qwen2.5-3b-instruct/chat-plain"
	const key = "test-key"
	eng := fakeengine.VLLM(t)
	eng.Next(fakeengine.Replay(t, rec))
	plat := fakeplatform.New(t, key)
	gw := fakegateway.New(t, key)

	cfg := &config.Config{}
	cfg.Provider.URL = plat.URL
	cfg.Provider.APIKey = key
	cfg.Provider.Engine = "vllm"
	s := &Server{config: cfg, llmClient: llm.NewClient("vllm", eng.URL, 30*time.Second)}
	s.SetMaxInflight(1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = connect.New(gw.URL, key, s.inferenceMux(), nil).Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	gw.AwaitSession(t)

	reqBody, err := os.ReadFile(filepath.Join(fakeengine.CorpusDir(), filepath.FromSlash(rec)+".request.json"))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "hmac-1")
	req.Header.Set("X-Inferoute-Timeout-Ms", "600000")
	resp, err := gw.Do(req)
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
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, want) {
		t.Fatalf("the gateway read %d %d bytes; the recording is %d bytes, and they differ", resp.StatusCode, len(got), len(want))
	}
	if reqs := eng.Requests(); len(reqs) != 1 || !bytes.Equal(reqs[0].Body, reqBody) {
		t.Fatalf("the engine received %d requests; the relay must forward the body unchanged", len(reqs))
	}
}
