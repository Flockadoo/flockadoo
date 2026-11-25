package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"errors"

	"github.com/Flockadoo/flockadoo/internal/admission"
	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/relay"
	"github.com/Flockadoo/flockadoo/pkg/capacity"
	"github.com/Flockadoo/flockadoo/pkg/connect"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/gpu"
	"github.com/Flockadoo/flockadoo/pkg/health"
	"github.com/Flockadoo/flockadoo/pkg/llm"
	"github.com/Flockadoo/flockadoo/pkg/logger"
	"github.com/Flockadoo/flockadoo/pkg/usermsg"
	"github.com/Flockadoo/flockadoo/pkg/verify"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
)

// maskString Helper function to mask sensitive strings
func maskStringHelper(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}

// Creates a new server
func CreateServer(ctx context.Context, cfg *config.Config, gpuMonitor *gpu.Monitor, healthReporter *health.Reporter, verifier *verify.Verifier) *Server {
	llmClient := llm.NewClient(cfg.PlatformType(), cfg.Provider.LLMURL, cfg.LLMTimeout())

	s := &Server{
		config:           cfg,
		gpuMonitor:       gpuMonitor,
		healthReporter:   healthReporter,
		llmClient:        llmClient,
		ctx:              ctx,
		sessionQueueWait: cfg.SessionQueueWait(),
	}
	if verifier != nil {
		// Not a typed nil inside the interface: a nil verifier serves all.
		s.verifier = verifier
	}
	s.sessionQueueLimit = cfg.Server.SessionQueueLimit
	kind, _ := engine.ParseKind(cfg.Provider.Engine)
	n := int32(engine.DefaultConcurrency(kind))
	if v, ok := cfg.ExplicitConcurrency(); ok {
		n = int32(v)
	}
	s.SetMaxInflight(n)

	return s
}

// SetCapacityNotify is called when inflight or the limit changes.
func (s *Server) SetCapacityNotify(fn func()) {
	s.onCapacity = fn
}

// admit is the admission gate, created on first use with a limit of 1 until
// SetMaxInflight sets the real one.
func (s *Server) admit() *admission.Gate {
	s.gateOnce.Do(func() {
		one, _ := admission.NewLimit(1)
		s.gate = admission.NewGate(one, s.notifyCapacityChange)
	})
	return s.gate
}

// SetMaxInflight changes the live slot limit and reports it immediately. A
// limit below 1 is refused (decision DC4): config rejects one at startup, and
// the benchmark never measures one.
func (s *Server) SetMaxInflight(n int32) {
	l, err := admission.NewLimit(int(n))
	if err != nil {
		logger.Error("refused a concurrency limit", zap.Error(err))
		return
	}
	s.admit().SetLimit(l)
}

// SetBenchmarking closes the gate while a concurrency benchmark runs.
func (s *Server) SetBenchmarking(on bool) {
	s.setClosed(func() { s.benchmarking = on })
	s.redrawConsole()
}

// SetCapacitySender is the sender the drain flushes its final report with.
func (s *Server) SetCapacitySender(c *capacity.Sender) {
	s.capacity = c
}

// setClosed applies change to the reasons the gate is closed, and closes or
// opens the gate to match: it is closed while a benchmark runs, the engine
// is down or the client is draining (invariant I7).
func (s *Server) setClosed(change func()) {
	s.closeMu.Lock()
	change()
	closed := s.benchmarking || s.engineDown || s.draining
	s.closeMu.Unlock()
	s.admit().SetClosed(closed)
}

// closedReason is why the gate is closed, for the dashboard.
func (s *Server) closedReason() string {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	switch {
	case s.draining:
		return "draining"
	case s.engineDown:
		return "engine down"
	case s.benchmarking:
		return "benchmarking"
	}
	return ""
}

// engineProbeInterval is how often WatchEngine looks at the engine.
const engineProbeInterval = 5 * time.Second

// WatchEngine looks at the engine every interval until ctx ends: two
// failures in a row close the gate as "engine down", one success opens it
// again (finding NET-7). It reports the engine down; it does not restart it
// (decision DC5). A request that cannot reach the engine asks for a look at
// once.
func (s *Server) WatchEngine(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = engineProbeInterval
	}
	s.closeMu.Lock()
	if s.engineProbe == nil {
		s.engineProbe = make(chan struct{}, 1)
	}
	probeNow := s.engineProbe
	s.closeMu.Unlock()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-probeNow:
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := s.llmClient.ListModels(pctx)
		cancel()
		switch {
		case err == nil:
			failures = 0
			s.setEngineDown(false)
		case ctx.Err() == nil:
			if failures++; failures >= 2 {
				s.setEngineDown(true)
			}
		}
	}
}

func (s *Server) setEngineDown(down bool) {
	s.closeMu.Lock()
	was := s.engineDown
	s.closeMu.Unlock()
	if was == down {
		return
	}
	if down {
		logger.Error("the engine does not answer; not accepting requests until it does")
	} else {
		logger.Info("the engine answers again; accepting requests")
	}
	s.setClosed(func() { s.engineDown = down })
}

// suspectEngine asks WatchEngine to look at the engine now.
func (s *Server) suspectEngine() {
	s.closeMu.Lock()
	ch := s.engineProbe
	s.closeMu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// defaultDrainLimit is how long a drain waits for requests in progress: the
// gateway's own drain time (decision DC9).
const defaultDrainLimit = 60 * time.Second

// Drain stops taking work and lets the work in progress finish: it closes
// the gate, sends a final "not accepting" report and waits for it, then
// waits for requests in flight, for up to limit or until ctx ends (a second
// stop request). What still runs then is cut by Stop (finding NET-3).
func (s *Server) Drain(ctx context.Context, limit time.Duration) error {
	if limit <= 0 {
		limit = defaultDrainLimit
	}
	// The console redraws itself every second; the new state shows there.
	s.setClosed(func() { s.draining = true })
	if s.capacity != nil {
		fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.capacity.Flush(fctx); err != nil {
			logger.Warn("the final capacity report failed", zap.Error(err))
		}
		cancel()
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for s.admit().Snapshot().Inflight > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%d requests still running after %v", s.admit().Snapshot().Inflight, limit)
		case <-tick.C:
		}
	}
	return nil
}

// SetEngineNote is the local dashboard line for benchmark progress.
func (s *Server) SetEngineNote(note string) {
	s.engineNoteMu.Lock()
	s.engineNote = note
	s.engineNoteMu.Unlock()
	s.redrawConsole()
}

// SetOrphanedEngines is the local dashboard line for orphaned vLLM engine
// processes the start found (engine start memory E4).
func (s *Server) SetOrphanedEngines(note string) {
	s.engineNoteMu.Lock()
	s.orphanedEngines = note
	s.engineNoteMu.Unlock()
	s.redrawConsole()
}

// SetMemoryShare is the local dashboard line for the memory share vLLM was
// started with on unified memory (engine start memory E2).
func (s *Server) SetMemoryShare(note string) {
	s.engineNoteMu.Lock()
	s.memoryShare = note
	s.engineNoteMu.Unlock()
	s.redrawConsole()
}

// SetCapabilities is the local dashboard line for capability checks.
// Session status stays online while this line is set.
func (s *Server) SetCapabilities(note string) {
	s.engineNoteMu.Lock()
	s.capabilitiesNote = note
	s.engineNoteMu.Unlock()
	s.redrawConsole()
}

// SlotSnapshot is what the capacity sender reports.
func (s *Server) SlotSnapshot() (inflight, maxInflight int32, accepting bool) {
	snap := s.admit().Snapshot()
	// Accepting only with an open gate and a connected tunnel (invariant
	// I7). A worker has no tunnel of its own.
	c := s.tunnel()
	return snap.Inflight, snap.Limit, snap.Accepting && (c == nil || c.Connected())
}

// SetConsoleUI enables or disables the ANSI terminal dashboard.
// Windows tray mode turns this off so the process can hide its console.
func (s *Server) SetConsole(w io.Writer) {
	s.console = w
}

// Start starts the local dashboard and the outbound provider connection.
// An enrolled worker has no provider API key that the gateway will accept.
// Start and Stop may be called from different goroutines, in either order
// (finding NET-6): Stop before Start finishes stops it.
func (s *Server) Start() error {
	connectCtx, connectCancel := context.WithCancel(s.ctx)
	s.lifeMu.Lock()
	if s.stopped {
		s.lifeMu.Unlock()
		connectCancel()
		return nil
	}
	s.connectCancel = connectCancel
	var connector *connect.Client
	if !s.config.EnrolledWorker() {
		connector = connect.New(s.config.Provider.URL, s.config.Provider.APIKey, s.inferenceMux(), s.onConnect)
		connector.OnState = func(connect.State) { s.notifyCapacityChange() }
		s.connector = connector
	}
	s.lifeMu.Unlock()

	if connector != nil {
		go func() {
			if err := connector.Run(connectCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("provider connection stopped", zap.Error(err))
			}
		}()
		// Wait up to 15 s for the first dial. A rejected key does not stop
		// the client: it stays up and the dashboard shows why (decision DC8).
		readyCtx, readyCancel := context.WithTimeout(connectCtx, 15*time.Second)
		_ = connector.Ready(readyCtx)
		readyCancel()
	}

	// Create server. Read/write timeouts must cover the session queue wait
	// plus inference so the orchestrator's sticky timeout is usable.
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", s.config.Server.Host, s.config.Server.Port),
		Handler:           s.localMux(),
		ReadTimeout:       s.config.RequestTimeout(),
		WriteTimeout:      s.config.RequestTimeout(),
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.lifeMu.Lock()
	if s.stopped {
		s.lifeMu.Unlock()
		return nil
	}
	s.server = srv
	s.lifeMu.Unlock()

	if s.console != nil {
		s.printStartupBanner()
		go s.consoleUpdater()
	}

	// Log server start
	logger.Info("Starting HTTP server",
		zap.String("address", srv.Addr),
		zap.String("provider_type", s.config.PlatformType()),
		zap.String("llm_url", s.config.Provider.LLMURL))

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop stops the server. It is safe before, during and after Start.
func (s *Server) Stop(ctx context.Context) error {
	logger.Info("Stopping HTTP server")
	s.lifeMu.Lock()
	s.stopped = true
	cancel, srv := s.connectCancel, s.server
	s.lifeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// tunnel is the provider connection, or nil (a worker, or before Start).
func (s *Server) tunnel() *connect.Client {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.connector
}

// localMux is the local listener's routes: the dashboard and its APIs. It
// serves no inference: that arrives only on the tunnel (decision DC3), and a
// provider who wants their own model locally talks to their engine.
func (s *Server) localMux() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/", s.handleDashboard).Methods(http.MethodGet)
	r.HandleFunc("/api/status", s.handleStatus).Methods(http.MethodGet)
	r.HandleFunc("/api/health", s.handleHealth).Methods(http.MethodGet)
	r.HandleFunc("/api/busy", s.handleBusy).Methods(http.MethodGet)
	return r
}

func (s *Server) inferenceMux() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/v1/chat/completions", s.handleChatCompletions).Methods(http.MethodPost)
	r.HandleFunc("/v1/completions", s.handleCompletions).Methods(http.MethodPost)
	r.HandleFunc("/v1/embeddings", s.handleEmbeddings).Methods(http.MethodPost)
	return r
}

func (s *Server) onConnect() {
	if s.healthReporter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if err := s.healthReporter.SendHealthReport(ctx); err != nil {
		logger.Error("health report after connect failed", zap.Error(err))
	}
}

func (s *Server) gatewayConnected() bool {
	c := s.tunnel()
	return c != nil && c.Connected()
}

// TunnelActive is false once the tunnel has stopped for good (a rejected
// key, a replacement, an upgrade required): the client then stops reporting
// to the server and waits for the operator (decision DC8).
func (s *Server) TunnelActive() bool {
	c := s.tunnel()
	return c == nil || !c.State().Terminal()
}

// consoleUpdater periodically updates the console with request stats and errors
func (s *Server) consoleUpdater() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.redrawConsole()
	}
}

// redrawConsole completely redraws the console. Benchmark progress calls
// this immediately; the 3s ticker is too slow and was painting the
// "verified" line only after the benchmarking flag had already cleared.
func (s *Server) redrawConsole() {
	if s.console == nil {
		return
	}
	s.consoleMu.Lock()
	defer s.consoleMu.Unlock()
	snap := s.snapshot(s.ctx)

	var buf bytes.Buffer
	buf.WriteString("\033[H\033[2J")
	buf.WriteString("\033[1;36m╔════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║                     FLOCKADOO PROVIDER CLIENT                    ║\n")
	buf.WriteString("╚════════════════════════════════════════════════════════════════╝\033[0m\n")

	fmt.Fprintf(&buf, "\033[1;35mLast Health Update            \033[0m%s\n", snap.LastHealthUpdate)
	statusColor := "\033[1;32m"
	if snap.SessionStatus == "benchmarking" {
		statusColor = "\033[1;33m"
	}
	fmt.Fprintf(&buf, "\033[1;35mSession Status                \033[0m%s%s\033[0m\n", statusColor, snap.SessionStatus)
	if snap.NotServing != "" {
		fmt.Fprintf(&buf, "\033[1;35m%-30s\033[0m\033[1;31m%s\033[0m\n", "Not serving", snap.NotServing)
	}
	if snap.EngineNote != "" {
		fmt.Fprintf(&buf, "\033[1;35mBenchmark                     \033[0m%s\n", snap.EngineNote)
	}
	if snap.CapabilityLine != "" {
		fmt.Fprintf(&buf, "\033[1;35mCapabilities                  \033[0m%s\n", snap.CapabilityLine)
	}
	if snap.OrphanedEngines != "" {
		fmt.Fprintf(&buf, "\033[1;35m%-30s\033[0m\033[1;33m%s\033[0m\n", "Orphaned engines", snap.OrphanedEngines)
	}
	switch snap.UpdateStatus {
	case "required":
		fmt.Fprintf(&buf, "\033[1;31mUpdate required: %s -> %s. Run 'flockadoo update' then restart.\033[0m\n", snap.ClientVersion, snap.LatestVersion)
	case "available":
		fmt.Fprintf(&buf, "\033[1;33mUpdate available: %s -> %s. Run 'flockadoo update' then restart.\033[0m\n", snap.ClientVersion, snap.LatestVersion)
	}
	fmt.Fprintf(&buf, "\033[1;35mEngine                        \033[0m%s\n", snap.ProviderType)
	if snap.MemoryShare != "" {
		fmt.Fprintf(&buf, "\033[1;35m%-30s\033[0m%s\n", "Memory share", snap.MemoryShare)
	}
	fmt.Fprintf(&buf, "\033[1;35mProvider API Key              \033[0m%s\n", snap.ProviderAPIKey)
	fmt.Fprintf(&buf, "\033[1;35mPlatform URL                  \033[0m%s\n", snap.ProviderURL)
	fmt.Fprintf(&buf, "\033[1;35mLLM URL                       \033[0m%s\n", snap.LLMURL)
	state := snap.PlatformConnection
	if snap.PlatformConnectionFailed {
		state = "\033[1;31m" + state + "\033[0m"
	}
	fmt.Fprintf(&buf, "\033[1;35mPlatform connection           \033[0m%s\n", state)

	s.writeFlockStatus(s.ctx, &buf, snap)

	gpuInfo := snap.GPU
	if gpuInfo == nil {
		gpuInfo = &gpu.GPUInfo{ProductName: "Unknown", DriverVersion: "Unknown", CUDAVersion: "Unknown"}
	}
	buf.WriteString("\033[1;36m╔════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║                          GPU INFORMATION                         ║\n")
	buf.WriteString("╚════════════════════════════════════════════════════════════════╝\033[0m\n")
	fmt.Fprintf(&buf, "\033[1;35mGPU                          \033[0m%s\n", gpuInfo.ProductName)
	fmt.Fprintf(&buf, "\033[1;35mDriver Version               \033[0m%s\n", gpuInfo.DriverVersion)
	fmt.Fprintf(&buf, "\033[1;35mCUDA Version                 \033[0m%s\n", gpuInfo.CUDAVersion)
	fmt.Fprintf(&buf, "\033[1;35mGPU Count                    \033[0m%d\n", gpuInfo.GPUCount)

	buf.WriteString("\n\033[1;33mRecent Requests:\033[0m\n")
	if len(snap.RecentRequests) == 0 {
		buf.WriteString("No requests yet\n")
	} else {
		for _, req := range snap.RecentRequests {
			buf.WriteString(formatRequestLine(req) + "\n")
		}
	}

	_, _ = buf.WriteTo(s.console)
}

func formatRequestLine(req RequestLog) string {
	var statusColor string
	switch {
	case req.Status >= 200 && req.Status < 300:
		statusColor = "\033[1;32m"
	case req.Status == 401:
		statusColor = "\033[1;33m"
	default:
		statusColor = "\033[1;31m"
	}
	return fmt.Sprintf("%s UTC %s %s %s%d\033[0m %s",
		req.Time, req.Method, req.Path, statusColor, req.Status, req.Duration)
}

func (s *Server) engineStatus(ctx context.Context) string {
	if s.config == nil {
		return ""
	}
	kind, _ := engine.ParseKind(s.config.Provider.Engine)
	switch s.config.Provider.FlockRole {
	case "worker":
		if engine.WorkerAlive(kind, s.config.Provider.EngineArgs) {
			return "running"
		}
		return "not started"
	case "master":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, peers := winglink.Status(ctx)
		return engine.MasterState(ctx, kind, s.config.Provider.LLMURL, s.config.Provider.EngineArgs, peers)
	default:
		return ""
	}
}

// flockLabel is how the screens show this machine's stored role: Solo for a
// one-machine flock, Team · master or Team · worker for a Team Flock
// (docs/naming.md in internal-docs). The status API keeps the stored value.
func flockLabel(role string) string {
	switch role {
	case "master":
		return "Team · master"
	case "worker":
		return "Team · worker"
	default:
		return "Solo"
	}
}

func (s *Server) writeFlockStatus(ctx context.Context, buf *bytes.Buffer, snap StatusSnapshot) {
	buf.WriteString("\033[1;36m╔════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║                           FLOCK STATUS                           ║\n")
	buf.WriteString("╚════════════════════════════════════════════════════════════════╝\033[0m\n")

	fmt.Fprintf(buf, "\033[1;35m%-30s\033[0m%s\n", "Flock", flockLabel(snap.Flock))
	if line := s.engineStatus(ctx); line != "" {
		fmt.Fprintf(buf, "\033[1;35m%-30s\033[0m%s\n", "Engine state", line)
	}
	if snap.Nodes != "" {
		fmt.Fprintf(buf, "\033[1;35m%-30s\033[0m%s\n", "Nodes", snap.Nodes)
	}
	if snap.WinglinkIP != "" {
		fmt.Fprintf(buf, "\033[1;35m%-30s\033[0m%s\n", "Winglink IP", snap.WinglinkIP)
	}

	models := snap.Models
	if len(models) == 0 {
		fmt.Fprintf(buf, "\033[1;35m%-30s\033[0m\033[1;33m(awaiting health sync)\033[0m\n", "Model")
		return
	}

	for i, m := range models {
		prefix := "Model"
		if i > 0 {
			prefix = ""
		}
		fmt.Fprintf(buf, "\033[1;35m%-30s\033[0m%s\n", prefix, m.ID)
		if m.VerificationStatus != "" {
			_, color := usermsg.ApprovalConsole(m.VerificationStatus)
			fmt.Fprintf(buf, "\033[1;35m%-30s\033[0m%s%s\033[0m\n", "Marketplace approval", color, m.MarketplaceApproval)
		}
	}
}

// printStartupBanner prints a nice startup banner with GPU info
func (s *Server) printStartupBanner() {
	// Just use the redrawConsole method to avoid duplication
	s.redrawConsole()
}

// logRequest logs a request to the console
func (s *Server) logRequest(method, path string, statusCode int, outcome relay.Outcome, startTime time.Time) {
	duration := time.Since(startTime)

	// Format the log entry
	switch {
	case outcome == relay.Completed && statusCode >= 200 && statusCode < 300:
		s.requestStats.mutex.Lock()
		s.requestStats.Success++
		s.requestStats.mutex.Unlock()
	case statusCode == 401:
		s.requestStats.mutex.Lock()
		s.requestStats.Unauthorized++
		s.requestStats.mutex.Unlock()
	default:
		s.requestStats.mutex.Lock()
		s.requestStats.Errors++
		s.requestStats.mutex.Unlock()
	}

	timestamp := time.Now().Format("15:04:05.000")
	entry := RequestLog{
		Time:     timestamp,
		Method:   method,
		Path:     path,
		Status:   statusCode,
		Duration: formatDuration(duration),
		Outcome:  outcome.String(),
	}

	s.requestStats.mutex.Lock()
	s.requestStats.Total++
	s.requestStats.LastRequests = append(s.requestStats.LastRequests, entry)
	if len(s.requestStats.LastRequests) > 10 {
		s.requestStats.LastRequests = s.requestStats.LastRequests[1:]
	}
	s.requestStats.mutex.Unlock()

	// Log to zap logger
	logger.Info("Request processed",
		zap.String("method", method),
		zap.String("path", path),
		zap.Int("status", statusCode),
		zap.String("outcome", outcome.String()),
		zap.Duration("duration", duration))
}

// logError writes a request failure to error.log. The terminal and dashboard do not show it.
func (s *Server) logError(errMsg string) {
	logger.Error(errMsg)
}

// verifyModel is the verdict for the model a request names, read from the
// verifier's state with no call to the server (plan item B3). With no
// verifier, every model is served.
func (s *Server) verifyModel(model string) (verify.Verdict, string) {
	if s.verifier == nil {
		return verify.Serve, ""
	}
	if model == "" {
		return verify.Refuse, "missing model in request"
	}
	return s.verifier.Verdict(model)
}
