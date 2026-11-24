package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/benchmark"
	"github.com/Flockadoo/flockadoo/pkg/capacity"
	"github.com/Flockadoo/flockadoo/pkg/capprobe"
	"github.com/Flockadoo/flockadoo/pkg/compat"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/gpu"
	"github.com/Flockadoo/flockadoo/pkg/health"
	"github.com/Flockadoo/flockadoo/pkg/llm"
	"github.com/Flockadoo/flockadoo/pkg/logger"
	"github.com/Flockadoo/flockadoo/pkg/nodeapi"
	"github.com/Flockadoo/flockadoo/pkg/server"
	"github.com/Flockadoo/flockadoo/pkg/setup"
	"github.com/Flockadoo/flockadoo/pkg/tray"
	"github.com/Flockadoo/flockadoo/pkg/update"
	"github.com/Flockadoo/flockadoo/pkg/usermsg"
	"github.com/Flockadoo/flockadoo/pkg/verify"
	"github.com/Flockadoo/flockadoo/pkg/version"
	"github.com/Flockadoo/flockadoo/pkg/winglink"
	"go.uber.org/zap"
)

//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico ../pkg/tray/icon.ico -o rsrc_windows_amd64.syso

const helpText = `
Flockadoo Client - A client for connecting to the Flockadoo network

Usage:
  flockadoo [flags]
  flockadoo setup [flags]
  flockadoo compatibility [flags]
  flockadoo update [--check]
  flockadoo benchmark
  flockadoo model download|login

Commands:
  setup           Interactive engine, model, and API key wizard (safe to re-run)
  compatibility   Detect local hardware and list which approved models can run
                  (does not start the provider daemon)
  update          Download the newest client, verify it, and replace this binary.
                  Stop the running client first. --check only prints the result.
  benchmark       Ask the running client to measure concurrency again.
  model           download: fetch a model at a commit the catalog lists into the
                  Hugging Face cache (default: the configured model, current
                  commit). login: log in to Hugging Face for gated models.

Flags:
  --config string   Path to configuration file (default: ~/.config/flockadoo/config.yaml)
  --tray            Windows only: run in the notification area (default on Windows)
  --console         Show the terminal dashboard (Windows: do not use the tray)
  --version         Show version information
  --help            Show this help message

For more information, visit: https://github.com/flockadoo/flockadoo
`

func main() {
	enableVirtualTerminal()
	// Until the config file is read, log to the default files. Library code
	// logs nowhere unless main installs a logger (finding TST-1).
	early := installEarlyLogger()
	defer func() { _ = early.Close() }()

	// Subcommands that must not start the provider daemon.
	if len(os.Args) > 1 && os.Args[1] == "compatibility" {
		if err := compat.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "compatibility: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "update" {
		if err := runUpdate(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "update: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		if err := setup.Run(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "setup: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "model" {
		if err := setup.RunModel(context.Background(), os.Args[2:], setup.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}); err != nil {
			fmt.Fprintf(os.Stderr, "model: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "benchmark" {
		if err := runBenchmark(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "benchmark: %v\n", err)
			os.Exit(1)
		}
		return
	}

	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.Usage = func() {
		fmt.Print(helpText)
	}

	configPath := flags.String("config", "", "Path to configuration file")
	trayFlag := flags.Bool("tray", false, "Windows only: run in the notification area (default on Windows)")
	consoleFlag := flags.Bool("console", false, "Show the terminal dashboard (disables Windows tray mode)")
	showVersion := flags.Bool("version", false, "Show version information")

	if err := flags.Parse(os.Args[1:]); err != nil {
		flags.Usage()
		os.Exit(1)
	}

	if flags.NArg() > 0 && (flags.Arg(0) == "help" || flags.Arg(0) == "--help") {
		flags.Usage()
		os.Exit(0)
	}

	if *showVersion {
		fmt.Printf("flockadoo %s\n", version.Version)
		fmt.Printf("  commit: %s\n", version.Commit)
		fmt.Printf("  built:  %s\n", version.Date)
		os.Exit(0)
	}

	fatal := func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}

	if *configPath == "" {
		resolved, err := resolveConfigPath()
		if err != nil {
			fatal("%s", err.Error())
		}
		*configPath = resolved
	}

	if err := maybeFirstRunSetup(*configPath); err != nil {
		fatal("%s", err.Error())
	}

	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		fmt.Printf("Configuration file %s not found, using defaults\n", *configPath)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("Failed to load configuration: %v", err)
	}
	config.ApplyEnvOverrides(cfg)
	update.RemoveStaleBackup()
	checkClientVersion(cfg.Provider.URL)
	if !cfg.HasAPIKey() && !cfg.EnrolledWorker() {
		fatal("%s\nOr run: flockadoo setup", usermsg.InvalidAPIKey)
	}

	useTray := tray.Supported() && !*consoleFlag
	if *trayFlag && !tray.Supported() {
		fmt.Fprintln(os.Stderr, "--tray is only supported on Windows; using console")
	}

	if !isTrayChild() {
		if err := setup.EnsureOnStartAt(context.Background(), cfg, *configPath, setup.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: inference engine not ready: %v\n", err)
		}
	}

	if useTray && spawnDetachedIfNeeded(cfg.LocalDashboardURL()) {
		return
	}

	fatal = func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintln(os.Stderr, msg)
		if useTray {
			showErrorDialog(msg)
		}
		os.Exit(1)
	}

	log, err := logger.New(&cfg.Logging)
	if err != nil {
		fatal("Failed to initialize logger: %v", err)
	}
	logger.SetDefaultLogger(log)
	// The configured logger may write to the same files; release the early
	// one's handles so the two never rotate the same file.
	_ = early.Close()
	defer func() { _ = log.Sync() }()

	logger.Info("Starting Flockadoo Provider Client",
		zap.String("config_path", *configPath),
		zap.String("log_level", cfg.Logging.Level),
		zap.String("log_dir", cfg.Logging.LogDir),
		zap.String("engine", cfg.Provider.Engine),
		zap.String("platform_url", cfg.Provider.URL),
		zap.Bool("tray", useTray))

	if useTray {
		hideConsole()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchClientVersion(ctx, cfg.Provider.URL)

	gpuMonitor, err := gpu.NewMonitor()
	if err != nil {
		logger.Error("Failed to initialize GPU monitor", zap.Error(err))
		logger.Warn("Continuing without GPU monitoring")
	}

	llmURL := cfg.Provider.LLMURL
	if cfg.EnrolledWorker() {
		if u := engine.MasterURL(cfg.Provider.EngineArgs); u != "" {
			llmURL = u
		}
	}
	llmClient := llm.NewClient(cfg.PlatformType(), llmURL, cfg.LLMTimeout())
	// A worker's own rank is headless; the master's setup checks the release.
	if !cfg.EnrolledWorker() {
		if msg := engineVersionWarning(ctx, cfg, llmClient); msg != "" {
			fmt.Fprintf(os.Stderr, "warning: the client will not serve through this engine: %s\n", msg)
			logger.Error("Not serving: the running engine is not supported", zap.String("reason", msg))
		}
	}

	catalog := verify.NewCatalog(cfg.Provider.URL, cfg.PlatformType())
	var modelVerifier *verify.Verifier
	if !cfg.EnrolledWorker() {
		if err := catalog.Refresh(ctx); err != nil {
			logger.Warn("Failed to fetch approved model catalog; verification may be limited", zap.Error(err))
		} else {
			logger.Info("Loaded approved model catalog",
				zap.Strings("aliases", catalog.Aliases()))
		}
		serverClient := verify.NewServerClient(cfg.Provider.URL, cfg.Provider.APIKey)
		modelVerifier = verify.NewVerifier(catalog, serverClient, cfg.PlatformType(), cfg.Provider.HFHubCache, cfg.Provider.ModelPath)
		// The commit the engine serves (model revisions C5), chosen at start.
		modelVerifier.SetRevision(cfg.Provider.HFRevision)
	}

	healthReporter := health.NewReporter(cfg, gpuMonitor, llmClient)
	healthReporter.SetVerifier(modelVerifier)
	// Model verification is refreshed in the background; requests only read
	// the verdict (plan item B3).
	if modelVerifier != nil {
		go modelVerifier.RunRefresher(ctx, 15*time.Second, func() []string { return []string{cfg.Provider.Model} })
	}

	srv := server.CreateServer(ctx, cfg, gpuMonitor, healthReporter, modelVerifier)
	if !useTray {
		srv.SetConsole(os.Stdout)
	}
	startCapacity(ctx, cfg, *configPath, llmURL, llmClient, gpuMonitor, catalog, srv, healthReporter)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Start()
	}()

	go func() {
		interval := health.ReportInterval
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		upgradeLogged := false

		report := func(msg string) bool {
			// A tunnel stopped for good (rejected key, replaced, upgrade
			// required) stops the reports too (decision DC8).
			if !srv.TunnelActive() {
				return true
			}
			err := healthReporter.SendHealthReport(ctx)
			if setup.NodeRemoved(err) {
				logger.Error("The owner removed this machine from the flock. Re-run setup and paste a new join code.", zap.Error(err))
				return false
			}
			next := nodeapi.HealthInterval(err, health.ReportInterval)
			if next != interval {
				interval = next
				ticker.Reset(interval)
			}
			if err == nil {
				upgradeLogged = false
				return true
			}
			if nodeapi.IsUpgradeRequired(err) {
				if !upgradeLogged {
					logger.Error(msg, zap.Error(err))
					upgradeLogged = true
				}
				return true
			}
			logger.Error(msg, zap.Error(err))
			return true
		}

		if ctx.Err() != nil {
			return
		}
		if !report("Failed to send initial health report") {
			return
		}

		for {
			select {
			case <-ticker.C:
				if !report("Failed to send health report") {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Start() waits up to 15s for the first gateway dial, then blocks in
	// ListenAndServe. A fatal start-up error (a rejected key, a port in use)
	// arrives on serverErr and must end the process, not leave it in the tray.
	var (
		exitMu  sync.Mutex
		exitErr error
	)
	setExitErr := func(err error) {
		if !isFatalServerErr(err) {
			return
		}
		exitMu.Lock()
		if exitErr == nil {
			exitErr = err
		}
		exitMu.Unlock()
	}

	if useTray {
		// Start can fail in tens of ms, before tray.Run has an event loop.
		// systray.Quit is a no-op until then, so drain first and fatal
		// without entering the tray. Slow failures wait for Started.
		select {
		case err := <-serverErr:
			if isFatalServerErr(err) {
				fatal("%s", usermsg.Startup(err))
			}
		default:
		}
		trayStarted := make(chan struct{})
		go func() {
			select {
			case <-quit:
			case err := <-serverErr:
				if isFatalServerErr(err) {
					setExitErr(err)
					logger.Error("Server failed", zap.Error(err))
					showErrorDialog(usermsg.Startup(err))
				}
			}
			<-trayStarted
			tray.Quit()
		}()
		tray.Run(tray.Options{
			ConfigPath:   *configPath,
			LogDir:       cfg.Logging.LogDir,
			DashboardURL: cfg.LocalDashboardURL(),
			Started:      trayStarted,
		})
	} else {
		select {
		case <-quit:
		case err := <-serverErr:
			if isFatalServerErr(err) {
				setExitErr(err)
				logger.Error("Server failed", zap.Error(err))
				fmt.Fprintln(os.Stderr, usermsg.Startup(err))
			}
		}
	}

	// Drain: take no new work, tell the server, and let what is running finish
	// for up to 60 s. A second interrupt stops at once (decision DC9).
	logger.Info("Draining: no new requests; waiting up to 60s for those in progress (interrupt again to stop now)")
	drainCtx, drainCancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-quit:
			drainCancel()
		case <-drainCtx.Done():
		}
	}()
	if err := srv.Drain(drainCtx, 0); err != nil {
		logger.Warn("drain ended before every request finished", zap.Error(err))
	}
	drainCancel()

	logger.Info("Shutting down gracefully...")
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := srv.Stop(stopCtx); err != nil {
		logger.Fatal("Server shutdown failed", zap.Error(err))
	}

	exitMu.Lock()
	err = exitErr
	exitMu.Unlock()
	if err != nil {
		os.Exit(1)
	}
}

func isFatalServerErr(err error) bool {
	return err != nil && !errors.Is(err, http.ErrServerClosed)
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "print whether an update is available")
	configPath := fs.String("config", "", "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	platform := ""
	path := *configPath
	if path == "" {
		path, _ = resolveConfigPath()
	}
	if path != "" {
		if cfg, err := config.Load(path); err == nil {
			config.ApplyEnvOverrides(cfg)
			platform = cfg.Provider.URL
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return update.Apply(ctx, platform, *checkOnly, os.Stdout)
}

func watchClientVersion(ctx context.Context, platformURL string) {
	if os.Getenv("FLOCKADOO_IN_DOCKER") == "1" || platformURL == "" {
		return
	}
	const timeout = 30 * time.Second
	var lastNotice string
	var warned bool
	check := func() {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		rel, err := update.FetchRelease(reqCtx, platformURL)
		if err != nil {
			if !warned {
				logger.Warn("version check skipped", zap.Error(err))
				warned = true
			}
			return
		}
		warned = false
		update.Remember(rel)
		msg, _ := update.Notice(rel)
		if msg == lastNotice {
			return
		}
		lastNotice = msg
		if msg != "" {
			logger.Info(msg)
		}
	}
	check()
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func checkClientVersion(platformURL string) {
	if os.Getenv("FLOCKADOO_IN_DOCKER") == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rel, err := update.FetchRelease(ctx, platformURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "version check skipped: %v\n", err)
		return
	}
	update.Remember(rel)
	msg, required := update.Notice(rel)
	if msg == "" {
		return
	}
	if required {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
	fmt.Println(msg)
}

func resolveConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	locations := []string{
		filepath.Join(homeDir, ".config", "flockadoo", "config.yaml"),
		"config.yaml",
	}
	for _, location := range locations {
		if _, err := os.Stat(location); err == nil {
			return location, nil
		}
	}
	return locations[0], nil
}

func maybeFirstRunSetup(configPath string) error {
	needs := false
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		needs = true
	} else if err != nil {
		return err
	} else {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		needs = !cfg.HasAPIKey() && !cfg.EnrolledWorker()
	}
	if !needs {
		return nil
	}
	if !setup.IsInteractive() {
		return fmt.Errorf("no valid configuration at %s\nRun: flockadoo setup", configPath)
	}
	fmt.Println("No provider API key configured. Starting setup...")
	return setup.Run([]string{"--config", configPath})
}

func runBenchmark(args []string) error {
	fs := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *configPath
	if path == "" {
		var err error
		path, err = resolveConfigPath()
		if err != nil {
			return err
		}
	}
	if err := benchmark.Clear(path); err != nil {
		return err
	}
	fmt.Println("benchmark requested; the running client measures on its next check")
	return nil
}

func startCapacity(ctx context.Context, cfg *config.Config, configPath, llmURL string, llmClient llm.Client, gpuMonitor *gpu.Monitor, catalog *verify.Catalog, srv *server.Server, reporter *health.Reporter) {
	if cfg.EnrolledWorker() || cfg.Provider.FlockRole == "worker" {
		return
	}
	sender := capacity.NewSender(cfg.Provider.URL, cfg.Provider.APIKey, srv.SlotSnapshot, llmClient)
	sender.Active = srv.TunnelActive
	srv.SetCapacitySender(sender)
	// A dead engine is reported, not restarted (decision DC5).
	go srv.WatchEngine(ctx, 0)
	srv.SetCapacityNotify(sender.Notify)
	go sender.Loop(ctx)

	// vLLM without a configured limit measures its concurrency. Close the gate
	// before the probes start, so their first round cannot delay that.
	skipped := benchmarkSkipNote(cfg)
	benchmarks := skipped == ""
	if benchmarks {
		srv.SetBenchmarking(true)
	} else {
		srv.SetEngineNote(skipped)
	}
	go (&capprobe.Watcher{
		EngineURL:   llmURL,
		Model:       cfg.Provider.Model,
		PlatformURL: cfg.Provider.URL,
		APIKey:      cfg.Provider.APIKey,
		Fingerprint: func() string { return deploymentFingerprint(ctx, cfg, llmClient, gpuMonitor) },
		// Not accepting means the concurrency benchmark owns the engine.
		Idle: func() bool {
			inflight, _, accepting := srv.SlotSnapshot()
			return inflight == 0 && accepting
		},
		// Each probe holds a slot, so the benchmark waits for it.
		Claim: srv.ClaimIdleSlot,
		Note:  srv.SetCapabilities,
		// A reasoning model is checked for its thinking coming back apart
		// from the answer; the server routes it only to providers that pass.
		Reasons: cfg.Provider.ReasoningParser != "",
		// The checks are chat requests; an embedding model answers none.
		Embedding: engine.IsEmbedding(cfg.Provider.Task),
	}).Loop(ctx)

	if !benchmarks {
		return
	}
	kind, _ := engine.ParseKind(cfg.Provider.Engine)
	go (&benchmark.Watcher{
		ConfigPath: configPath,
		EngineURL:  llmURL,
		Model:      cfg.Provider.Model,
		Ready: func() bool {
			probe, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if cfg.Provider.FlockRole == "master" {
				_, online := winglink.Status(probe)
				return engine.MasterState(probe, kind, llmURL, cfg.Provider.EngineArgs, online) == "ready"
			}
			return engine.Healthy(probe, kind, llmURL)
		},
		Fingerprint: func() string { return deploymentFingerprint(ctx, cfg, llmClient, gpuMonitor) },
		Recommended: func() int {
			entry, ok := catalog.Get(cfg.Provider.Model)
			if ok && entry.RecommendedConcurrency != nil && *entry.RecommendedConcurrency > 0 {
				return int(*entry.RecommendedConcurrency)
			}
			return engine.DefaultConcurrency(kind)
		},
		Apply: func(n int) { srv.SetMaxInflight(int32(n)) },
		Note:  srv.SetEngineNote,
		Gate:  srv.SetBenchmarking,
		Inflight: func() int32 {
			n, _, _ := srv.SlotSnapshot()
			return n
		},
		GPUBusy: func() bool {
			if gpuMonitor == nil {
				return false
			}
			info, err := gpuMonitor.GetGPUInfo(ctx)
			return err == nil && info != nil && info.Utilization > 20
		},
		Preemptions: func(c context.Context) int64 {
			src, ok := llmClient.(interface {
				EngineCapacity(context.Context) (llm.EngineCapacity, error)
			})
			if !ok {
				return 0
			}
			got, err := src.EngineCapacity(c)
			if err != nil || got.Preemptions == nil {
				return 0
			}
			return *got.Preemptions
		},
		// An embedding model answers no chat; it is measured with embeddings.
		Embedding: engine.IsEmbedding(cfg.Provider.Task),
		Record: func(res benchmark.Result) {
			reporter.SetBenchmark(&health.BenchmarkSummary{
				Concurrency: res.Concurrency,
				MeasuredAt:  res.MeasuredAt,
				Levels:      res.Levels,
			})
		},
	}).Loop(ctx)
}

// deploymentFingerprint identifies what is being served: model, engine and its
// version, weights, GPUs, and the engine's launch arguments. It is empty while
// the engine does not answer, so a half-started engine is not mistaken for a
// different deployment.
// benchmarkSkipNote is the Benchmark line when the concurrency benchmark does
// not run, and "" when it does. A limit written into config is used as
// written; llama.cpp splits its context across slots, so it keeps its fixed
// default instead of measuring.
func benchmarkSkipNote(cfg *config.Config) string {
	if n, ok := cfg.ExplicitConcurrency(); ok {
		return fmt.Sprintf("limit of %d set in config", n)
	}
	if cfg.Provider.Engine == string(engine.KindLlamaCpp) {
		return fmt.Sprintf("fixed limit of %d", engine.DefaultConcurrency(engine.KindLlamaCpp))
	}
	return ""
}

func deploymentFingerprint(ctx context.Context, cfg *config.Config, llmClient llm.Client, gpuMonitor *gpu.Monitor) string {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	listed, err := llmClient.ListModels(probe)
	if err != nil {
		return ""
	}
	// The version the running engine reports (finding ENG-4): an upgrade
	// changes the fingerprint, and no process is started to find it.
	fp := cfg.Provider.Model + "|" + cfg.Provider.Engine + "|" + runningEngineVersion(probe, cfg, llmClient)
	for _, m := range listed.Models {
		if m.ID == cfg.Provider.Model || cfg.Provider.Model == "" {
			fp += "|" + m.WeightFingerprint + "|" + fmt.Sprint(m.MaxModelLen)
			break
		}
	}
	if gpuMonitor != nil {
		if info, err := gpuMonitor.GetGPUInfo(ctx); err == nil && info != nil {
			fp += "|" + info.ProductName + "|" + fmt.Sprint(info.GPUCount)
		}
	}
	fp += "|" + fmt.Sprint(engine.NNodes(cfg.Provider.EngineArgs))
	fp += "|" + engineArgsDigest(cfg.Provider.EngineArgs)
	// A single machine has no saved launch arguments; its engine is started
	// with the config's parsers, so turning one on is a new deployment too.
	fp += "|" + cfg.Provider.ToolCallParser + "|" + cfg.Provider.ReasoningParser
	return fp
}

// engineVersionWarning is why the client will not serve through the engine
// already running (a vLLM older than engine.MinVLLMVersion), or "".
func engineVersionWarning(ctx context.Context, cfg *config.Config, client llm.Client) string {
	kind, ok := engine.ParseKind(cfg.Provider.Engine)
	if !ok {
		return ""
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := engine.CheckVersion(kind, runningEngineVersion(probe, cfg, client)); err != nil {
		return err.Error()
	}
	return ""
}

// runningEngineVersion asks the running engine for its release: /version on
// vLLM, /props on llama.cpp. An engine client without the lookup gives "".
func runningEngineVersion(ctx context.Context, cfg *config.Config, client llm.Client) string {
	v, ok := client.(interface {
		EngineVersion(ctx context.Context, llamaServer bool) string
	})
	if !ok {
		return ""
	}
	return v.EngineVersion(ctx, cfg.Provider.Engine == string(engine.KindLlamaCpp))
}

// engineArgsDigest changes when the engine's launch arguments change (a tool
// parser turned on, say). It is a hash because the fingerprint is sent to the
// platform and the arguments can hold an API key.
func engineArgsDigest(args []string) string {
	sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(sum[:6])
}

// installEarlyLogger installs the default file logger, so the subcommands
// and the start of the daemon log where they always have. It returns the
// logger so main can close its files once the configured one replaces it.
// If the files cannot be opened, logging stays off.
func installEarlyLogger() *logger.Logger {
	l, err := logger.New(logger.DefaultConfig())
	if err != nil {
		return nil
	}
	logger.SetDefaultLogger(l)
	return l
}
