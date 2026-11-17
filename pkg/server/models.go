package server

import (
	"github.com/sentnl/inferoute-node/inferoute-client/internal/admission"
	"net/http"
	"sync"
	"time"

	"context"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/connect"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/gpu"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/health"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/verify"
)

// Server represents the HTTP server
type Server struct {
	config         *config.Config
	gpuMonitor     *gpu.Monitor
	healthReporter *health.Reporter
	llmClient      llm.Client
	verifier       *verify.Verifier
	connector      *connect.Client
	connectCancel  context.CancelFunc
	consoleUI      bool
	consoleMu      sync.Mutex
	// gate admits requests to the engine; the count lives inside it (plan
	// item B1). admit creates it on first use.
	gateOnce         sync.Once
	gate             *admission.Gate
	onCapacity       func()
	engineNote       string
	engineNoteMu     sync.Mutex
	capabilitiesNote string
	sessionQueueWait time.Duration
	// sessionQueueLimit is how many returning turns may wait for a slot at
	// once; beyond it a turn is refused at once. Zero means the default.
	sessionQueueLimit int
	// engineIdleLimit ends an engine answer that sends nothing for this
	// long; zero means defaultEngineIdleLimit.
	engineIdleLimit time.Duration
	// sessions remembers recent X-Session-Key values so a returning
	// conversation can wait for a slot. There is no per-session lock:
	// parallel agent calls often share one key, and serialising them
	// would throw away the batching the slot limit exists for.
	sessionMu    sync.Mutex
	sessions     map[string]time.Time
	server       *http.Server
	requestStats struct {
		Total        int
		Success      int
		Errors       int
		Unauthorized int
		LastRequests []RequestLog
		mutex        sync.Mutex
	}
}

// BusyResponse is the response structure for the busy endpoint
type BusyResponse struct {
	Busy bool `json:"busy"`
}

// ErrorResponse is the standard error response structure
type ErrorResponse struct {
	Error string `json:"error"`
}
