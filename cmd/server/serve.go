package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/observeworker"
	"github.com/kingfs/Trajecta/internal/proxy"
	"github.com/kingfs/Trajecta/internal/reanalysis"
	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/internal/responses/functionexec"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	authMigrateDatabaseUp = auth.MigrateDatabaseUp
	authOpenDatabase      = auth.OpenDatabase
)

func newServeCommand(runtime *cliRuntime) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the proxy, recorder, monitor, and MCP management endpoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCode(func() int {
				return runServeWithConfig(runtime.configPath())
			})
		},
	}
}

func runServe(args []string) int {
	configPath, code := parseConfigPath("serve", args)
	if code != 0 {
		return code
	}
	return runServeWithConfig(configPath)
}

func parseConfigPath(name string, args []string) (string, int) {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	configPath := fs.StringP("config", "c", "config.yaml", "Path to configuration file")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(normalizeLegacyFlagArgs(args)); err != nil {
		return "", 2
	}
	return *configPath, 0
}

func runServeWithConfig(configPath string) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("Failed to load config", "path", configPath, "error", err)
		return 1
	}
	if err := applyStartupProviderProbeSuggestions(context.Background(), cfg, nil); err != nil {
		slog.Error("Startup provider probe failed", "error", err)
		return 1
	}
	if err := validateServeConfig(cfg); err != nil {
		slog.Error("Invalid serve config", "error", err)
		return 1
	}

	if cfg.DatabaseAutoMigrate() {
		// Announce it before blocking. Migrations run ahead of the first
		// "Starting LLM Proxy..." line, so a migration that takes minutes - or
		// that never returns, which is what a pathological plan looks like from
		// the outside - otherwise presents as a process that started cleanly,
		// printed nothing, and sat at 0% CPU while the database burned a core.
		slog.Info("Applying application database migrations...", "driver", cfg.DatabaseDriver())
		if err := migrateApplicationDatabaseUp(cfg, 0); err != nil {
			slog.Error("Failed to migrate application database", "error", err)
			return 1
		}
	}

	slog.Info("Starting LLM Proxy...", "version", Version, "go_version", "1.25+")

	authStore, err := openAuthStore(cfg)
	if err != nil {
		slog.Error("Failed to initialize auth store", "error", err)
		return 1
	}
	defer authStore.Close()

	traceStore, err := store.NewWithDatabaseOptions(
		cfg.TraceOutputDir(),
		cfg.DatabaseDriver(),
		cfg.DatabaseDSN(),
		cfg.DatabaseMaxOpenConns(),
		cfg.DatabaseMaxIdleConns(),
		store.DatabaseOptions{
			AutoMigrate:           false,
			UseSessionSummaryRead: cfg.DatabaseUseSessionSummaryRead(),
			// The Monitor's list and analytics reads share this process with the
			// proxy. A separate, bounded pool keeps a cold page - a 30-day
			// Overview measured 14 s on this deployment's rotational disk - from
			// holding every connection the proxy needs to persist a recording.
			ReadMaxOpenConns:     cfg.DatabaseReadMaxOpenConns(),
			ReadMaxIdleConns:     cfg.DatabaseReadMaxIdleConns(),
			ReadStatementTimeout: cfg.DatabaseReadStatementTimeout(),
		},
	)
	if err != nil {
		slog.Error("Failed to initialize trace store", "error", err)
		return 1
	}
	defer func() {
		// The derived read models are maintained through a deferred queue, so a
		// clean shutdown settles it before the database handle goes away.
		traceStore.FlushDerivedRefresh()
		traceStore.Close()
	}()
	syncCtx, cancelSync := context.WithCancel(context.Background())
	var background sync.WaitGroup
	defer func() {
		cancelSync()
		background.Wait()
	}()
	parseWorker := observeworker.New(traceStore, observeworker.Options{Interval: 5 * time.Second, BatchSize: 10})
	background.Add(1)
	go func() {
		defer background.Done()
		parseWorker.Run(syncCtx)
	}()
	analysisWorker := reanalysis.NewWorker(traceStore, reanalysis.WorkerOptions{Interval: 5 * time.Second, BatchSize: 5})
	background.Add(1)
	go func() {
		defer background.Done()
		analysisWorker.Run(syncCtx)
	}()

	channelService := channel.NewService(traceStore)
	if imported, err := channelService.BootstrapFromConfig(cfg); err != nil {
		slog.Error("Failed to bootstrap channel config", "error", err)
		return 1
	} else if imported > 0 {
		slog.Info("Imported upstream config into channel store", "channels", imported)
	}
	routerCfg, source, err := routerConfigFromChannels(cfg, channelService)
	if err != nil {
		slog.Error("Failed to build router config from channels", "error", err)
		return 1
	}
	slog.Info("Resolved router config source", "source", source)
	if err := validateServeRouterConfig(cfg, routerCfg); err != nil {
		// Non-fatal by design: without an eligible chat completions upstream the
		// local Responses server simply cannot serve /v1/responses, but the
		// process must still start so operators can reach the management UI and
		// fix channel configuration. Request-time routing reports the concrete
		// per-request failure when no Responses route is available.
		slog.Warn("Local Responses server has no eligible chat completions upstream; starting anyway", "error", err)
	}

	rtr, err := router.New(routerCfg, traceStore)
	if err != nil {
		slog.Error("Invalid upstream config", "error", err)
		return 1
	}
	if err := rtr.Initialize(); err != nil {
		slog.Error("Failed to initialize upstream router", "error", err)
		return 1
	}
	defer rtr.Close()
	rtr.StartBackgroundRefresh()
	logResolvedTargets(rtr)

	functionExecutorManager, err := buildResponsesFunctionExecutorManager(context.Background(), cfg, traceStore)
	if err != nil {
		slog.Error("Failed to initialize responses function executor registry", "error", err)
		return 1
	}

	var managementSrv *http.Server
	if cfg.Monitor.Port != "" {
		mux := newManagementMuxWithFunctionExecutorManager(traceStore, rtr, cfg, functionExecutorManager, authStore)
		managementSrv = newManagementHTTPServer(cfg, mux)
		go func(srv *http.Server) {
			addr := srv.Addr
			slog.Info("Management server started", "addr", addr, "monitor_url", "http://localhost"+addr, "mcp_path", effectiveMCPPath(cfg))
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("Monitor server failed", "error", err)
			}
		}(managementSrv)
	}

	handler, err := proxy.NewHandlerWithAuth(cfg, traceStore, rtr, authStore, functionExecutorManager)
	if err != nil {
		slog.Error("Failed to create proxy handler", "error", err)
		return 1
	}

	// The periodic walk is a reconciliation backstop, not the normal path: a
	// cassette is indexed when it is written, and Sync only has to catch files
	// that appeared while the process was not running. On a large install every
	// pass re-reads every prelude, so the interval is configurable and defaults to
	// the much longer trace.sync_interval rather than five minutes.
	startTraceStoreBackgroundSync(syncCtx, traceStore, cfg.TraceSyncInterval(), &background)

	// Finalising a recording rewrites the whole cassette to put its prelude in
	// front of the record, then writes the trace index row and enqueues the parse
	// job. That work is as large as the response body, so it runs on a bounded
	// background queue instead of on the request goroutine, which would otherwise
	// hold the client connection long after the last byte was delivered. The
	// queue falls back to finalising inline when it is full, so a recording is
	// never dropped for the sake of latency.
	handler.StartFinalizeWorkers()

	// The derived read models (session summaries) are rebuilt by a background
	// flusher rather than by whichever read happens to arrive next. The write path
	// already defers that rebuild because it costs about as much as the rest of the
	// recording; without this the cost simply moved to a page render.
	traceStore.StartDerivedRefresh()

	addr := ":" + cfg.Server.Port
	srv := newProxyHTTPServer(cfg, handler)

	// SIGTERM/SIGINT drain the in-flight requests before the deferred cleanup runs.
	// Without this the process died on the first signal: http.Server never stopped
	// accepting, the request that was being proxied was cut mid-stream, and every
	// deferred step below - settling the derived read models, closing the store,
	// stopping the parse and analysis workers - was skipped, so a recording that had
	// been written but not yet finalised with its prelude stayed unusable. A second
	// signal ends the process immediately, which is what an operator reaching for a
	// stuck shutdown expects.
	shutdownCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-shutdownCtx.Done()
		stopSignals()
		slog.Info("Shutdown signal received, draining in-flight requests", "timeout", gracefulShutdownTimeout)
		gracefulShutdown(gracefulShutdownTimeout, srv, managementSrv)
	}()

	slog.Info("Server listening", "addr", addr, "trace_output_dir", cfg.TraceOutputDir(), "database_driver", cfg.DatabaseDriver())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("Server failed", "error", err)
		return 1
	}

	// The HTTP servers have stopped, so nothing new can be queued. Drain the
	// finalize queue before the deferred cleanup closes the store: finalising a
	// recording writes its index row and enqueues its parse job, and both need a
	// live store. A recording that is still a record-first fragment after a hard
	// kill cannot be repaired, because the metadata that becomes its prelude only
	// ever existed in memory.
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), recorder.FinalizeTimeout)
	defer cancelFinalize()
	if err := handler.FinalizeRecordings(finalizeCtx); err != nil {
		slog.Warn("Recorder finalize queue did not drain cleanly", "error", err)
	}
	if stats := handler.FinalizeStats(); stats.Queued > 0 || stats.Inline > 0 {
		slog.Info("Recorder finalize queue drained", "queued", stats.Queued, "inline", stats.Inline, "still_queued", stats.Depth)
	}
	return 0
}

// gracefulShutdownTimeout bounds how long a drain may take. The container's own grace
// period is the outer bound: a stream that legitimately runs longer than this is cut
// here, and one that runs longer than the grace period is killed by the runtime.
const gracefulShutdownTimeout = 30 * time.Second

// gracefulShutdown stops each server from accepting new connections and waits for the
// requests already in flight to finish, up to timeout. Anything still running when the
// timeout expires is dropped, so a stuck upstream cannot hold the process open.
func gracefulShutdown(timeout time.Duration, servers ...*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		wg.Add(1)
		go func(srv *http.Server) {
			defer wg.Done()
			if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				slog.Warn("HTTP server shutdown did not complete cleanly", "addr", srv.Addr, "error", err)
			}
		}(srv)
	}
	wg.Wait()
}

// newProxyHTTPServer builds the HTTP server that serves proxied model traffic.
//
// It carries no write deadline by default. `http.Server.WriteTimeout` spans the
// whole response write rather than the gap between writes, so a fixed value
// truncates every response that legitimately runs longer, which for this server
// is the normal case: a proxied chat completion, the local Responses runtime's
// SSE stream and a long reasoning turn all can. The client saw the connection
// close mid-body with no protocol error it could interpret. What bounds a
// request instead is the caller (the outbound upstream request carries the
// incoming request context, so a cancelled SDK call cancels the upstream call)
// plus the transport's dial and TLS timeouts. An operator that wants a hard cap
// sets `server.write_timeout`.
func newProxyHTTPServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.ServerReadTimeout(),
		WriteTimeout:      cfg.ServerWriteTimeout(),
		IdleTimeout:       2 * time.Minute,
	}
}

// newManagementHTTPServer builds the Monitor/API/MCP server. It carries no write
// deadline because `GET /api/events/stream` is a long-lived SSE response that a
// fixed deadline closed on schedule.
func newManagementHTTPServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.Monitor.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       2 * time.Minute,
	}
}

func buildResponsesFunctionExecutorManager(ctx context.Context, cfg *config.Config, traceStore *store.Store) (*functionexec.Manager, error) {
	base := cfg.ResponsesFunctionExecutorsConfig()
	if traceStore != nil {
		snapshot, ok, err := traceStore.LoadResponsesFunctionExecutorConfigSnapshot(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			base = functionexec.ApplySafeOverlay(base, snapshot)
			slog.Info("Loaded persisted responses function executor overlay")
		}
	}
	return functionexec.NewManager(base)
}

func startTraceStoreBackgroundSync(ctx context.Context, traceStore *store.Store, interval time.Duration, wg *sync.WaitGroup) {
	if traceStore == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		run := func(reason string) {
			start := time.Now()
			if err := traceStore.Sync(); err != nil {
				slog.Warn("Trace index background sync failed", "reason", reason, "error", err)
				return
			}
			slog.Info("Trace index background sync finished", "reason", reason, "duration", time.Since(start).String())
		}

		run("startup")
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run("periodic")
			}
		}
	}()
}

func openAuthStore(cfg *config.Config) (*auth.Store, error) {
	return openAuthStoreWithAutoSchema(cfg)
}

func openAuthStoreWithAutoSchema(cfg *config.Config) (*auth.Store, error) {
	driver := normalizeAuthStoreDriver(cfg.DatabaseDriver())
	switch driver {
	case "sqlite":
		if cfg.DatabaseAutoMigrate() {
			if err := authMigrateDatabaseUp(driver, cfg.DatabaseDSN(), 0); err != nil {
				return nil, fmt.Errorf("migrate database: %w", err)
			}
		}
	case "postgres":
		// Postgres auth tables are owned by the application migration set, which
		// serve applies before opening the auth store.
	default:
		return nil, fmt.Errorf("auth store driver %q is not supported yet", driver)
	}

	st, err := authOpenDatabase(
		driver,
		cfg.DatabaseDSN(),
		cfg.DatabaseMaxOpenConns(),
		cfg.DatabaseMaxIdleConns(),
	)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func normalizeAuthStoreDriver(driver string) string {
	driver = strings.ToLower(strings.TrimSpace(driver))
	switch driver {
	case "":
		// See store.normalizeDatabaseDriver: an unset driver means Postgres.
		return "postgres"
	case "postgresql":
		return "postgres"
	default:
		return driver
	}
}

func validateServeConfig(cfg *config.Config) error {
	switch driver := cfg.DatabaseDriver(); driver {
	case "sqlite", "postgres", "postgresql":
	default:
		return fmt.Errorf("database.driver %q is not supported yet; use sqlite or postgres", driver)
	}
	if cfg.MCP.Enabled && cfg.Monitor.Port == "" {
		return errors.New("monitor.port is required when mcp.enabled=true")
	}
	if cfg.MCP.Enabled {
		if _, err := normalizeMCPPath(cfg.MCP.Path); err != nil {
			return err
		}
	}
	return nil
}

// validateServeRouterConfig reports router-level configuration problems that
// make the local Responses server unusable. It is a preflight diagnostic, not a
// startup gate: callers log the returned error as a warning so the process still
// boots and the management UI stays reachable for reconfiguration.
func validateServeRouterConfig(cfg *config.Config, routerCfg *config.Config) error {
	if cfg != nil {
		if err := router.ValidateLocalResponsesServerBackendConfig(routerCfg); err != nil {
			return err
		}
	}
	return nil
}

func routerConfigFromChannels(cfg *config.Config, channelService *channel.Service) (*config.Config, string, error) {
	if configHasExplicitCredentials(cfg) {
		return cfg, "yaml", nil
	}
	targets, err := channelService.RuntimeTargets()
	if err != nil {
		return nil, "", err
	}
	initialized, err := channelService.HasConfiguration()
	if err != nil {
		return nil, "", err
	}
	if !initialized {
		return cfg, "yaml", nil
	}
	routerCfg := *cfg
	routerCfg.Upstream = config.UpstreamConfig{}
	routerCfg.Upstreams = targets
	return &routerCfg, "database", nil
}

func configHasExplicitCredentials(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, target := range cfg.EffectiveUpstreams() {
		if target.HasExplicitCredentials() {
			return true
		}
	}
	return false
}
