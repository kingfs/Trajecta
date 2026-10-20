package main

import (
	"crypto/sha256"
	"log/slog"
	"net/http"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/mcpserver"
	"github.com/kingfs/Trajecta/internal/monitor"
	"github.com/kingfs/Trajecta/internal/responses/functionexec"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newManagementMux(traceStore *store.Store, rtr *router.Router, cfg *config.Config, authStore ...*auth.Store) *http.ServeMux {
	return newManagementMuxWithFunctionExecutorManager(traceStore, rtr, cfg, nil, authStore...)
}

func newManagementMuxWithFunctionExecutorManager(
	traceStore *store.Store,
	rtr *router.Router,
	cfg *config.Config,
	functionExecutorManager *functionexec.Manager,
	authStore ...*auth.Store,
) *http.ServeMux {
	mux := http.NewServeMux()
	var authStorePtr *auth.Store
	var verifier auth.TokenVerifier
	var monitorJWT *auth.JWTManager
	var monitorVerifier auth.TokenVerifier
	if len(authStore) > 0 {
		authStorePtr = authStore[0]
	}
	if authStorePtr != nil {
		verifier = authStorePtr
		var err error
		monitorJWT, err = newMonitorJWTManager(traceStore, cfg.AuthSessionTTL())
		if err != nil {
			panic(err)
		}
		monitorVerifier = monitorJWT
	}
	if cfg.MCP.Enabled {
		server := mcpserver.New(traceStore, mcpserver.Options{Router: rtr})
		mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, &mcp.StreamableHTTPOptions{
			// MCP 2026-07-28 is sessionless: peer information is carried in
			// per-request metadata and server/discover replaces initialization.
			// The SDK continues to negotiate legacy protocol revisions on this
			// endpoint for older clients.
			Stateless: true,
		})
		mux.Handle(config.EffectiveMCPPath(cfg), auth.Middleware(mcpHandler, "trajecta-mcp", verifier))
	}
	functionExecutorConfig := cfg.ResponsesFunctionExecutorsConfig()
	if functionExecutorManager != nil {
		functionExecutorConfig = functionExecutorManager.Config()
	}
	// The slow-query ring is off unless debug.slow_query_threshold is positive;
	// arming it here keeps the switch in one place next to the pprof switch.
	store.SetSlowQueryThreshold(cfg.DebugSlowQueryThreshold())
	// The build identity is injected at link time into this package (see the
	// Taskfile's LDFLAGS), so it is handed to the Monitor from here rather than
	// read there. The install ping is reported to the page verbatim: the browser
	// is what sends it, and the page has to be able to say whether it will.
	telemetry := cfg.TelemetrySettings()
	monitor.RegisterRoutes(mux, traceStore, monitor.RouteOptions{
		Router:                           rtr,
		ChannelService:                   channel.NewService(traceStore).WithReadOnly(configHasExplicitCredentials(cfg)),
		AuthVerifier:                     verifier,
		MonitorAuthVerifier:              monitorVerifier,
		MonitorJWT:                       monitorJWT,
		AuthStore:                        authStorePtr,
		SessionTTL:                       cfg.AuthSessionTTL(),
		ResponsesFunctionExecutors:       functionExecutorConfig,
		ResponsesFunctionExecutorStore:   traceStore,
		ResponsesFunctionExecutorManager: functionExecutorManager,
		Location:                         cfg.MonitorLocation(),
		DebugPprofEnabled:                cfg.DebugPprofEnabled(),
		About: monitor.AboutOptions{
			Build: monitor.BuildInfo{
				Name:    "Trajecta",
				Version: Version,
				Commit:  Commit,
				Date:    Date,
				Branch:  Branch,
			},
			UpdateCheckEnabled:  cfg.UpdateCheckEnabled(),
			UpdateCheckEndpoint: cfg.UpdateCheckEndpoint(),
			Telemetry: monitor.TelemetryOptions{
				Enabled:         telemetry.Enabled,
				Provider:        telemetry.Provider,
				Endpoint:        telemetry.Endpoint,
				PathPrefix:      telemetry.PathPrefix,
				IntervalSeconds: int(telemetry.Interval / time.Second),
			},
		},
	})
	return mux
}

func newMonitorJWTManager(traceStore *store.Store, ttl time.Duration) (*auth.JWTManager, error) {
	if traceStore == nil {
		return auth.NewEphemeralJWTManager(ttl)
	}
	key, _, err := traceStore.ExportLocalSecretKey()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("trajecta-monitor-jwt:"), key...))
	return auth.NewJWTManager(auth.JWTOptions{Secret: sum[:], TTL: ttl})
}

func effectiveMCPPath(cfg *config.Config) string {
	if !cfg.MCP.Enabled {
		return ""
	}
	return config.EffectiveMCPPath(cfg)
}

func logResolvedTargets(rtr *router.Router) {
	for _, target := range rtr.Targets() {
		diagnostics, err := target.Upstream.StartupDiagnostics()
		if err != nil {
			slog.Warn("Failed to build upstream startup diagnostics", "upstream_id", target.ID, "error", err)
			continue
		}
		slog.Info(
			"Resolved upstream target",
			"upstream_id", target.ID,
			"base_url", target.Upstream.BaseURL,
			"provider_preset", target.Upstream.ProviderPreset,
			"protocol_family", target.Upstream.ProtocolFamily,
			"routing_profile", target.Upstream.RoutingProfile,
			"api_version", target.Upstream.APIVersion,
			"deployment", target.Upstream.Deployment,
			"connectivity_endpoint", diagnostics.ConnectivityEndpoint,
			"connectivity_url", diagnostics.ConnectivityURL,
			"model_routing_hint", diagnostics.ModelRoutingHint,
		)
	}
}

func logResolvedUpstreamConfig(resolvedUpstream upstream.ResolvedUpstream, diagnostics upstream.StartupDiagnostics) {
	slog.Info(
		"Resolved upstream config",
		"base_url", resolvedUpstream.BaseURL,
		"provider_preset", resolvedUpstream.ProviderPreset,
		"protocol_family", resolvedUpstream.ProtocolFamily,
		"routing_profile", resolvedUpstream.RoutingProfile,
		"api_version", resolvedUpstream.APIVersion,
		"deployment", resolvedUpstream.Deployment,
		"connectivity_endpoint", diagnostics.ConnectivityEndpoint,
		"connectivity_url", diagnostics.ConnectivityURL,
		"model_routing_hint", diagnostics.ModelRoutingHint,
	)
}
