package server

import (
	"context"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakegateway"
)

// Plan item C1, decision DC8: a key rejected at start leaves the client up,
// with the reason on the dashboard; it does not exit.
func TestAKeyRejectedAtStartLeavesTheClientUp(t *testing.T) {
	gw := fakegateway.New(t, "the-right-key")
	cfg := &config.Config{}
	cfg.Provider.URL = gw.URL
	cfg.Provider.APIKey = "a-revoked-key"
	cfg.Provider.Engine = "vllm"
	cfg.Server.Host = "127.0.0.1"
	s := &Server{config: cfg}
	errc := make(chan error, 1)
	go func() { errc <- s.Start() }()
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	select {
	case err := <-errc:
		t.Fatalf("Start returned %v; a rejected key must leave the client up", err)
	case <-time.After(time.Second):
	}
	if snap := s.snapshot(); snap.Connection != "rejected" || snap.ConnectionReason == "" {
		t.Fatalf("dashboard connection %q (%q), want rejected with the reason", snap.Connection, snap.ConnectionReason)
	}
}
