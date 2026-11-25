package server

import (
	"github.com/Flockadoo/flockadoo/internal/admission"
	"github.com/Flockadoo/flockadoo/pkg/capacity"
	"io"
	"net/http"
	"sync"
	"time"

	"context"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/connect"
	"github.com/Flockadoo/flockadoo/pkg/gpu"
	"github.com/Flockadoo/flockadoo/pkg/health"
	"github.com/Flockadoo/flockadoo/pkg/llm"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Server represents the HTTP server
type Server struct {
	config         *config.Config
	gpuMonitor     *gpu.Monitor
	healthReporter *health.Reporter
	llmClient      llm.Client
	// verifier gives the verdict for a model; nil serves every model.
	verifier verdicts
	// lifeMu guards the lifecycle fields: Start and Stop may run in
	// different goroutines (finding NET-6).
	lifeMu        sync.Mutex
	stopped       bool
	connector     *connect.Client
	connectCancel context.CancelFunc
	// console is the terminal dashboard's writer; nil turns it off.
	console io.Writer
	// ctx is the client's lifetime, from CreateServer.
	ctx       context.Context
	consoleMu sync.Mutex
	// gate admits requests to the engine; the count lives inside it (plan
	// item B1). admit creates it on first use.
	gateOnce   sync.Once
	gate       *admission.Gate
	onCapacity func()
	// capacity is the capacity sender, for the drain's final report.
	capacity *capacity.Sender
	// closeMu guards why the gate is closed: a benchmark, a dead engine,
	// or a drain (invariant I7). engineProbe asks WatchEngine to look now.
	closeMu          sync.Mutex
	benchmarking     bool
	engineDown       bool
	draining         bool
	engineProbe      chan struct{}
	engineNote       string
	engineNoteMu     sync.Mutex
	capabilitiesNote string
	orphanedEngines  string // orphaned vLLM engine processes the start found (E4)
	engineNotReady   string // why the engine is not ready after the start
	memoryShare      string // the memory share vLLM was started with (E2)
	sessionQueueWait time.Duration
	// sessionQueueLimit is how many returning turns may wait for a slot at
	// once; beyond it a turn is refused at once. Zero means the default.
	sessionQueueLimit int
	// engineIdleLimit ends a streamed engine answer that sends nothing for
	// this long; zero means defaultEngineIdleLimit.
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

// verdicts is what the relay needs from model verification: the verdict for
// a model now, read from state (plan section 15.3).
type verdicts interface {
	Verdict(model string) (verify.Verdict, string)
}
