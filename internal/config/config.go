package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/redaction"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port string `yaml:"port"`
		// ReadTimeout bounds reading one request, including its body.
		ReadTimeout time.Duration `yaml:"read_timeout"`
		// WriteTimeout bounds writing one response. Zero means no server-side
		// write deadline; see Config.ServerWriteTimeout.
		WriteTimeout time.Duration `yaml:"write_timeout"`
		// UpstreamResponseHeaderTimeout bounds the wait for the response
		// headers of a forwarded request. Zero means no bound; see
		// Config.UpstreamResponseHeaderTimeout.
		UpstreamResponseHeaderTimeout time.Duration `yaml:"upstream_response_header_timeout"`
	} `yaml:"server"`

	Monitor struct {
		Port string `yaml:"port"`
	} `yaml:"monitor"`

	MCP struct {
		Enabled bool   `yaml:"enabled"`
		Path    string `yaml:"path"`
	} `yaml:"mcp"`

	Auth struct {
		DatabasePath string        `yaml:"database_path"`
		SessionTTL   time.Duration `yaml:"session_ttl"`
	} `yaml:"auth"`

	Database struct {
		Driver                string `yaml:"driver"`
		DSN                   string `yaml:"dsn"`
		MaxOpenConns          int    `yaml:"max_open_conns"`
		MaxIdleConns          int    `yaml:"max_idle_conns"`
		AutoMigrate           *bool  `yaml:"auto_migrate"`
		UseSessionSummaryRead *bool  `yaml:"use_session_summary_read"`
	} `yaml:"database"`

	Trace struct {
		OutputDir string `yaml:"output_dir"`
	} `yaml:"trace"`

	Upstream  UpstreamConfig         `yaml:"upstream"`
	Upstreams []UpstreamTargetConfig `yaml:"upstreams"`

	ProviderProbe ProviderProbeConfig `yaml:"provider_probe"`

	Router RouterConfig `yaml:"router"`

	Limits LimitConfig `yaml:"limits"`

	ResponsesServer ResponsesServerConfig `yaml:"responses_server"`

	Tools ToolsConfig `yaml:"tools"`

	Debug struct {
		OutputDir string `yaml:"output_dir"`
		// MaskKey replaces the credential-carrying request headers with placeholders
		// before the exchange is written to disk. It defaults to true (see Load); set
		// it to false to record the original values, which puts live credentials in
		// the cassette.
		MaskKey bool `yaml:"mask_key"`
	} `yaml:"debug"`

	// 新增 Chaos 配置
	Chaos struct {
		Enabled bool        `yaml:"enabled"`
		Rules   []ChaosRule `yaml:"rules"`
	} `yaml:"chaos"`
}

type UpstreamConfig struct {
	BaseURL        string                     `yaml:"base_url"`
	ApiKey         string                     `yaml:"api_key"`
	ProviderPreset string                     `yaml:"provider_preset"`
	APIType        string                     `yaml:"api_type"`
	Mode           string                     `yaml:"mode"`
	Capabilities   UpstreamCapabilitiesConfig `yaml:"capabilities"`
	// ModelCapabilities holds per-model capability overrides keyed by the
	// client-facing model name (or one of its aliases). A non-nil field on an
	// entry overrides the target-level api_type/capabilities for that model
	// only; models without an entry keep the target-level behaviour. This is
	// what lets one channel serve some models natively and others through a
	// different protocol surface.
	ModelCapabilities map[string]UpstreamCapabilitiesConfig `yaml:"model_capabilities"`
	ProtocolFamily    string                                `yaml:"protocol_family"`
	RoutingProfile    string                                `yaml:"routing_profile"`
	APIVersion        string                                `yaml:"api_version"`
	Deployment        string                                `yaml:"deployment"`
	Project           string                                `yaml:"project"`
	Location          string                                `yaml:"location"`
	ModelResource     string                                `yaml:"model_resource"`
	Headers           map[string]string                     `yaml:"headers"`
}

type UpstreamCapabilitiesConfig struct {
	Responses       *bool `yaml:"responses" json:"responses,omitempty"`
	ChatCompletions *bool `yaml:"chat_completions" json:"chat_completions,omitempty"`
	ToolCalling     *bool `yaml:"tool_calling" json:"tool_calling,omitempty"`
	Embeddings      *bool `yaml:"embeddings" json:"embeddings,omitempty"`
	Models          *bool `yaml:"models" json:"models,omitempty"`
	Tokenize        *bool `yaml:"tokenize" json:"tokenize,omitempty"`
}

type UpstreamTargetConfig struct {
	ID                   string             `yaml:"id"`
	Enabled              *bool              `yaml:"enabled"`
	Priority             int                `yaml:"priority"`
	Weight               float64            `yaml:"weight"`
	CapacityHint         float64            `yaml:"capacity_hint"`
	ModelDiscovery       string             `yaml:"model_discovery"`
	StaticModels         []string           `yaml:"static_models"`
	ModelAliases         map[string]string  `yaml:"-"`
	ConfiguredModelsOnly bool               `yaml:"-"`
	DisabledModels       []string           `yaml:"-"`
	AllowUnknownModels   *bool              `yaml:"allow_unknown_models"`
	Upstream             UpstreamConfig     `yaml:"upstream"`
	Credentials          []CredentialConfig `yaml:"credentials"`
}

type ProviderProbeConfig struct {
	StartupFill bool          `yaml:"startup_fill"`
	Timeout     time.Duration `yaml:"timeout"`
}

type CredentialConfig struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Enabled          *bool             `yaml:"enabled"`
	ApiKey           string            `yaml:"api_key"`
	Headers          map[string]string `yaml:"headers"`
	ConcurrencyLimit int               `yaml:"concurrency_limit"`
}

type RouterConfig struct {
	ModelDiscovery struct {
		Enabled         *bool         `yaml:"enabled"`
		RefreshInterval time.Duration `yaml:"refresh_interval"`
	} `yaml:"model_discovery"`
	Selection struct {
		Policy           string        `yaml:"policy"`
		Epsilon          float64       `yaml:"epsilon"`
		OpenWindow       time.Duration `yaml:"open_window"`
		FailureThreshold int64         `yaml:"failure_threshold"`
	} `yaml:"selection"`
	Fallback struct {
		OnMissingModel string `yaml:"on_missing_model"`
	} `yaml:"fallback"`
}

type LimitConfig struct {
	Enabled          bool   `yaml:"enabled"`
	Scope            string `yaml:"scope"`
	MaxConcurrent    int    `yaml:"max_concurrent"`
	MaxQueued        int    `yaml:"max_queued"`
	ChannelKeyHeader string `yaml:"channel_key_header"`
}

type ResponsesServerConfig struct {
	DefaultModel                string                          `yaml:"default_model"`
	ForceStore                  bool                            `yaml:"force_store"`
	MaxRequestBodyBytes         int64                           `yaml:"max_request_body_bytes"`
	Path                        string                          `yaml:"path"`
	AutoCompact                 bool                            `yaml:"auto_compact"`
	CompactHistoryItemThreshold int                             `yaml:"compact_history_item_threshold"`
	ModelProfiles               []ResponsesModelProfileConfig   `yaml:"model_profiles"`
	AdoptChannelModelProfiles   bool                            `yaml:"adopt_channel_model_profiles"`
	FunctionExecutors           ResponsesFunctionExecutorConfig `yaml:"function_executors"`
	CodexCompat                 ResponsesCodexCompatConfig      `yaml:"codex_compat"`
}

type ResponsesCodexCompatConfig struct {
	Enabled               bool     `yaml:"enabled"`
	AutoInjectHostedTools []string `yaml:"auto_inject_hosted_tools"`
	InjectWhenToolsAbsent *bool    `yaml:"inject_when_tools_absent"`
	PreserveClientTools   *bool    `yaml:"preserve_client_tools"`
	DefaultToolChoice     any      `yaml:"default_tool_choice"`
}

type ResponsesModelProfileConfig struct {
	Name                        string                         `yaml:"name"`
	Pattern                     string                         `yaml:"pattern"`
	ContextWindowTokens         int                            `yaml:"context_window_tokens"`
	MaxOutputTokens             int                            `yaml:"max_output_tokens"`
	ToolOutputTokenLimit        int                            `yaml:"tool_output_token_limit"`
	ModelReasoningEffort        string                         `yaml:"model_reasoning_effort"`
	CompactHistoryItemThreshold int                            `yaml:"compact_history_item_threshold"`
	UpstreamModel               string                         `yaml:"upstream_model"`
	TokenizeCounter             ResponsesTokenizeCounterConfig `yaml:"tokenize_counter"`
}

type ResponsesModelProfileMatch struct {
	Matched bool
	Index   int
	Kind    string
	Source  string
	Profile ResponsesModelProfileConfig
}

type ResponsesTokenizeCounterConfig struct {
	Enabled    *bool         `yaml:"enabled"`
	UpstreamID string        `yaml:"upstream_id"`
	Timeout    time.Duration `yaml:"timeout"`
}

type ResponsesFunctionExecutorConfig struct {
	Enabled        bool                               `yaml:"enabled"`
	Timeout        time.Duration                      `yaml:"timeout"`
	MaxResultBytes int                                `yaml:"max_result_bytes"`
	Redaction      ResponsesFunctionRedactionConfig   `yaml:"redaction"`
	Executors      []ResponsesFunctionExecutorBinding `yaml:"executors"`
	Warnings       []string                           `yaml:"-" json:"-"`
}

type ResponsesFunctionRedactionConfig struct {
	Arguments bool `yaml:"arguments"`
	Output    bool `yaml:"output"`
}

type ResponsesFunctionExecutorBinding struct {
	Name         string                                 `yaml:"name"`
	Type         string                                 `yaml:"type"`
	Enabled      *bool                                  `yaml:"enabled"`
	Output       any                                    `yaml:"output"`
	Command      string                                 `yaml:"command"`
	Args         []string                               `yaml:"args"`
	Timeout      time.Duration                          `yaml:"timeout"`
	Env          map[string]string                      `yaml:"env"`
	EnvAllowlist []string                               `yaml:"env_allowlist"`
	Process      ResponsesFunctionExecutorProcessConfig `yaml:"process"`
	Available    bool                                   `yaml:"-" json:"-"`
	Warnings     []string                               `yaml:"-" json:"-"`
}

type ResponsesFunctionExecutorProcessConfig struct {
	WorkingDir             string   `yaml:"working_dir"`
	RequireAbsoluteCommand bool     `yaml:"require_absolute_command"`
	AllowedCommandDirs     []string `yaml:"allowed_command_dirs"`
	RejectRoot             bool     `yaml:"reject_root"`
}

const (
	ResponsesFunctionExecutorTypeStaticResponse  = "static_response"
	ResponsesFunctionExecutorTypeExternalCommand = "external_command"
)

func SupportedResponsesFunctionExecutorTypes() []string {
	return []string{
		ResponsesFunctionExecutorTypeStaticResponse,
		ResponsesFunctionExecutorTypeExternalCommand,
	}
}

type ToolsConfig struct {
	WebSearch WebSearchToolConfig `yaml:"web_search"`
	MCP       MCPToolConfig       `yaml:"mcp"`
}

type WebSearchToolConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Provider   string `yaml:"provider"`
	MaxResults int    `yaml:"max_results"`
	BaseURL    string `yaml:"base_url"`
	TimeoutMS  int    `yaml:"timeout_ms"`
	UserAgent  string `yaml:"user_agent"`
}

type MCPToolConfig struct {
	Enabled          bool                  `yaml:"enabled"`
	DefaultTimeoutMS int                   `yaml:"default_timeout_ms"`
	MaxResultBytes   int                   `yaml:"max_result_bytes"`
	Servers          []MCPToolServerConfig `yaml:"servers"`
}

type MCPToolServerConfig struct {
	ID             string   `yaml:"id"`
	Label          string   `yaml:"label"`
	URL            string   `yaml:"url"`
	BearerTokenEnv string   `yaml:"bearer_token_env"`
	EnabledTools   []string `yaml:"enabled_tools"`
	DisabledTools  []string `yaml:"disabled_tools"`
	Enabled        *bool    `yaml:"enabled"`
}

func (s MCPToolServerConfig) EnabledOrDefault() bool {
	return s.Enabled == nil || *s.Enabled
}

func (c LimitConfig) LocalConcurrencyEnabled() bool {
	return c.Enabled && c.MaxConcurrent > 0
}

func (c LimitConfig) ScopeOrDefault() string {
	scope := strings.ToLower(strings.TrimSpace(c.Scope))
	if scope != "" {
		return scope
	}
	if strings.TrimSpace(c.ChannelKeyHeader) != "" {
		return "header"
	}
	return "global"
}

type ChaosRule struct {
	Model      string        `yaml:"model"`       // 针对的模型，"*" 代表所有
	Rate       float64       `yaml:"rate"`        // 概率 0.0 ~ 1.0
	Action     string        `yaml:"action"`      // "delay" 或 "error"
	Delay      time.Duration `yaml:"delay"`       // 延迟时间
	StatusCode int           `yaml:"status_code"` // 错误码
	Message    string        `yaml:"message"`     // 错误内容
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Masking is on unless the configuration says otherwise. SECURITY.md, README.md and
	// README_EN.md all document `debug.mask_key` as defaulting to true, but the zero value of
	// the field is false, so a configuration that simply omitted the key recorded the
	// client's Authorization, api-key, x-api-key and x-goog-api-key headers verbatim - in a
	// file that is written to disk, kept next to the database and routinely committed as test
	// data. Seeding the field before the YAML is decoded keeps an explicit
	// `mask_key: false` (or TRAJECTA_MASK_KEY=false) authoritative while an omitted key now
	// means the documented default.
	cfg := Config{}
	cfg.Debug.MaskKey = true
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	warnUnknownConfigKeys(data)
	applyEnvOverrides(&cfg)
	if err := expandEnvRefs(&cfg); err != nil {
		return nil, err
	}
	if err := validateLimits(&cfg); err != nil {
		return nil, err
	}
	if err := validateRouterEnums(&cfg); err != nil {
		return nil, err
	}
	if err := validateChaosRules(&cfg); err != nil {
		return nil, err
	}
	if err := validateFiniteNumbers(&cfg); err != nil {
		return nil, err
	}
	// Neither trace.output_dir nor debug.output_dir names a directory. That is allowed (the
	// SQLite default path is built from it, so an empty value means "next to the process"), but
	// it decides where every cassette is written, and the only trace of it used to be the
	// startup log line. Say it at load time instead.
	if strings.TrimSpace(cfg.TraceOutputDir()) == "" {
		slog.Warn("neither trace.output_dir nor debug.output_dir is set; recordings and the default SQLite database are written relative to the process working directory")
	}
	return &cfg, nil
}

// validChaosActions lists the actions internal/proxy implements. The proxy matches the action
// after internal/chaos normalizes it, and an action outside this set marks the rule as hit and
// then falls through both branches: the request is forwarded to the upstream as if chaos were
// disabled, which is the opposite of what the operator configured.
var validChaosActions = map[string]struct{}{
	"delay": {},
	"error": {},
}

// validateChaosRules rejects fault-injection rules that cannot do what they say, the same way
// Load rejects an unknown limit scope.
//
// `status_code` is checked against the range net/http accepts because the injected response is
// written through the normal upstream path: `WriteHeader` panics outside 100..999, so the rule
// that was meant to simulate a failing provider took the handler down and handed the client a
// torn connection instead of an error. `rate` is documented as 0.0 to 1.0, and a value above 1
// silently turned "inject on 2% of requests" into "inject on all of them".
func validateChaosRules(cfg *Config) error {
	for i, rule := range cfg.Chaos.Rules {
		where := fmt.Sprintf("chaos.rules[%d]", i)
		if model := strings.TrimSpace(rule.Model); model != "" && model != "*" {
			where = fmt.Sprintf("chaos.rules[%d] (%s)", i, model)
		}
		action := strings.TrimSpace(rule.Action)
		if action == "" {
			return fmt.Errorf("%s has no action; use one of delay, error", where)
		}
		if _, ok := validChaosActions[strings.ToLower(action)]; !ok {
			return fmt.Errorf("%s action %q is not supported; use one of delay, error", where, action)
		}
		if rule.Rate < 0 || rule.Rate > 1 {
			return fmt.Errorf("%s rate %v is outside the documented range 0.0 to 1.0", where, rule.Rate)
		}
		if status := rule.StatusCode; status != 0 && (status < 100 || status > 999) {
			return fmt.Errorf("%s status_code %d cannot be written by net/http; use 0 for the default (500) or a value between 100 and 999", where, status)
		}
	}
	return nil
}

// validateFiniteNumbers rejects non-finite numbers for every float knob in the configuration.
//
// YAML spells them as `.nan`, `.inf` and `-.inf`, and a non-finite value slips through every rule
// that is written as a comparison: `rate < 0 || rate > 1` is false for NaN, and the router's
// non-positive-means-default rule (`v <= 0`) is false for NaN too. The value then reaches the hot
// path, where it does not fail loudly but changes routing silently: weight and capacity_hint
// divide a target's expected cost, so a NaN cost makes the cost comparison a non-total order and
// the affected target wins against every other candidate, and the router snapshot can no longer
// be marshalled for the Monitor (`json: unsupported value: NaN`). Load refuses the value instead.
//
// The walk is reflective so a new float field cannot be added without being covered; the reported
// path uses the same yaml names the operator writes.
func validateFiniteNumbers(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	return validateFiniteValue(reflect.ValueOf(cfg), "")
}

func validateFiniteValue(value reflect.Value, path string) error {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return validateFiniteValue(value.Elem(), path)
	case reflect.Struct:
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" { // unexported
				continue
			}
			name := field.Name
			if tag := strings.Split(field.Tag.Get("yaml"), ",")[0]; tag != "" && tag != "-" {
				name = tag
			}
			child := path
			if child == "" {
				child = name
			} else {
				child = child + "." + name
			}
			if err := validateFiniteValue(value.Field(i), child); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateFiniteValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if err := validateFiniteValue(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key().Interface())); err != nil {
				return err
			}
		}
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("%s must be a finite number, got %v; YAML spells non-finite values as .nan, .inf or -.inf", path, number)
		}
	}
	return nil
}

// validLimitScopes lists the scopes internal/proxy actually honours. An unknown
// scope would silently disable limiting, so Load rejects it instead of ignoring it.
var validLimitScopes = map[string]struct{}{
	"global":       {},
	"header":       {},
	"channel":      {},
	"route_target": {},
	"credential":   {},
}

// warnUnknownConfigKeys reports YAML keys that no Config field reads.
//
// Loading deliberately stays non-strict so an existing deployment is not broken
// by an extra key, but silently ignoring unknown keys is what let removed
// options (for example responses_server.enabled or
// router.model_discovery.startup_policy) survive in config files and docs
// without any effect. Re-decoding with KnownFields(true) turns that drift into a
// visible startup warning instead of silence.
func warnUnknownConfigKeys(data []byte) {
	for _, detail := range unknownConfigKeys(data) {
		slog.Warn("configuration key is not read by this build and was ignored", "detail", detail)
	}
}

// unknownConfigKeys returns one message per YAML key that no Config field reads.
func unknownConfigKeys(data []byte) []string {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var probe Config
	err := decoder.Decode(&probe)
	if err == nil {
		return nil
	}
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return nil
	}
	return typeErr.Errors
}

func validateLimits(cfg *Config) error {
	if !cfg.Limits.Enabled {
		return nil
	}
	scope := cfg.Limits.ScopeOrDefault()
	if _, ok := validLimitScopes[scope]; !ok {
		return fmt.Errorf("limits.scope %q is not supported; use one of global, header, channel, route_target, credential", scope)
	}
	if scope == "header" && strings.TrimSpace(cfg.Limits.ChannelKeyHeader) == "" {
		return fmt.Errorf("limits.scope %q requires limits.channel_key_header", scope)
	}
	return nil
}

// validSelectionPolicies lists the values internal/router honours for
// router.selection.policy. The router normalizes an unknown value to p2c instead of
// failing, so a misspelling silently picks a different selection algorithm; Load rejects it
// the way it rejects an unknown limits.scope.
var validSelectionPolicies = map[string]struct{}{
	"p2c":             {},
	"first_available": {},
}

// validMissingModelPolicies lists the values internal/router honours for
// router.fallback.on_missing_model. `reject` is the strict value and every other string
// takes the permissive branch, so a misspelled `reject` silently allows a model no upstream
// declares instead of failing the request.
var validMissingModelPolicies = map[string]struct{}{
	"reject":   {},
	"fallback": {},
}

// validModelDiscoveryModes lists the values internal/router honours for a target's
// model_discovery. An unknown value is normalized to list_models, so a typo can switch the
// upstream model discovery back on for an operator who meant to turn it off.
var validModelDiscoveryModes = map[string]struct{}{
	"list_models": {},
	"static_only": {},
	"disabled":    {},
}

// validateRouterEnums rejects routing values that no consumer recognises.
//
// The router reads these through normalizing helpers (normalizePolicy, normalizeFallback,
// normalizeDiscoveryMode) that answer an unrecognised value with a default rather than an
// error, because a request-time normalization has no way to report a configuration mistake.
// At load time the mistake is still visible, and all three defaults differ from what the
// operator wrote, so ignoring it would change behaviour without a word. An empty value stays
// valid: it means "use the default".
func validateRouterEnums(cfg *Config) error {
	if value := strings.TrimSpace(cfg.Router.Selection.Policy); value != "" {
		if _, ok := validSelectionPolicies[strings.ToLower(value)]; !ok {
			return fmt.Errorf("router.selection.policy %q is not supported; use one of p2c, first_available", value)
		}
	}
	if value := strings.TrimSpace(cfg.Router.Fallback.OnMissingModel); value != "" {
		if _, ok := validMissingModelPolicies[strings.ToLower(value)]; !ok {
			return fmt.Errorf("router.fallback.on_missing_model %q is not supported; use one of reject, fallback", value)
		}
	}
	for i, target := range cfg.Upstreams {
		value := strings.TrimSpace(target.ModelDiscovery)
		if value == "" {
			continue
		}
		if _, ok := validModelDiscoveryModes[strings.ToLower(value)]; !ok {
			where := fmt.Sprintf("upstreams[%d]", i)
			if id := strings.TrimSpace(target.ID); id != "" {
				where = fmt.Sprintf("upstreams[%d] (%s)", i, id)
			}
			return fmt.Errorf("%s model_discovery %q is not supported; use one of list_models, static_only, disabled", where, value)
		}
	}
	return nil
}

// applyEnvOverrides applies the TRAJECTA_* environment overrides on top of the
// loaded configuration.
//
// Every variable is applied only when it is set and its value parses, so a
// malformed value leaves the loaded or default value in place instead of failing
// startup. Order is load-bearing where two variables write the same field, so the
// calls are in the order the blocks were written: TRAJECTA_OUTPUT_DIR seeds both
// output directories and TRAJECTA_TRACE_OUTPUT_DIR then overrides the trace one,
// and the bootstrap-upstream variables extend one entry in turn. The surface is 58
// variables, so each is one call rather than a near-identical block; the typed
// helpers that follow hold the parsing rules.
func applyEnvOverrides(cfg *Config) {
	envString(&cfg.Server.Port, "TRAJECTA_SERVER_PORT")
	envDuration(&cfg.Server.ReadTimeout, "TRAJECTA_SERVER_READ_TIMEOUT")
	envDuration(&cfg.Server.WriteTimeout, "TRAJECTA_SERVER_WRITE_TIMEOUT")
	envDuration(&cfg.Server.UpstreamResponseHeaderTimeout, "TRAJECTA_SERVER_UPSTREAM_RESPONSE_HEADER_TIMEOUT")
	envString(&cfg.Monitor.Port, "TRAJECTA_MONITOR_PORT")
	envBool(&cfg.MCP.Enabled, "TRAJECTA_MCP_ENABLED")
	envString(&cfg.MCP.Path, "TRAJECTA_MCP_PATH")
	envString(&cfg.Auth.DatabasePath, "TRAJECTA_AUTH_DATABASE_PATH")
	envDuration(&cfg.Auth.SessionTTL, "TRAJECTA_AUTH_SESSION_TTL")
	envString(&cfg.Database.Driver, "TRAJECTA_DATABASE_DRIVER")
	envString(&cfg.Database.DSN, "TRAJECTA_DATABASE_DSN")
	envInt(&cfg.Database.MaxOpenConns, "TRAJECTA_DATABASE_MAX_OPEN_CONNS")
	envInt(&cfg.Database.MaxIdleConns, "TRAJECTA_DATABASE_MAX_IDLE_CONNS")
	envBoolPtr(&cfg.Database.AutoMigrate, "TRAJECTA_DATABASE_AUTO_MIGRATE")
	envBoolPtr(&cfg.Database.UseSessionSummaryRead, "TRAJECTA_DATABASE_USE_SESSION_SUMMARY_READ")
	envUpstream(cfg, "TRAJECTA_UPSTREAM_BASE_URL", func(u *UpstreamConfig, v string) { u.BaseURL = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_API_KEY", func(u *UpstreamConfig, v string) { u.ApiKey = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_PROVIDER_PRESET", func(u *UpstreamConfig, v string) { u.ProviderPreset = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_API_TYPE", func(u *UpstreamConfig, v string) { u.APIType = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_MODE", func(u *UpstreamConfig, v string) { u.Mode = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_PROTOCOL_FAMILY", func(u *UpstreamConfig, v string) { u.ProtocolFamily = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_ROUTING_PROFILE", func(u *UpstreamConfig, v string) { u.RoutingProfile = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_API_VERSION", func(u *UpstreamConfig, v string) { u.APIVersion = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_DEPLOYMENT", func(u *UpstreamConfig, v string) { u.Deployment = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_PROJECT", func(u *UpstreamConfig, v string) { u.Project = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_LOCATION", func(u *UpstreamConfig, v string) { u.Location = v })
	envUpstream(cfg, "TRAJECTA_UPSTREAM_MODEL_RESOURCE", func(u *UpstreamConfig, v string) { u.ModelResource = v })
	envBootstrapUpstream(cfg, "TRAJECTA_BOOTSTRAP_UPSTREAM_BASE_URL", func(u *UpstreamConfig, v string) { u.BaseURL = v })
	envBootstrapUpstream(cfg, "TRAJECTA_BOOTSTRAP_UPSTREAM_API_KEY", func(u *UpstreamConfig, v string) { u.ApiKey = v })
	envBool(&cfg.ProviderProbe.StartupFill, "TRAJECTA_PROVIDER_PROBE_STARTUP_FILL")
	envDuration(&cfg.ProviderProbe.Timeout, "TRAJECTA_PROVIDER_PROBE_TIMEOUT")
	envStringPair(&cfg.Debug.OutputDir, &cfg.Trace.OutputDir, "TRAJECTA_OUTPUT_DIR")
	envString(&cfg.Trace.OutputDir, "TRAJECTA_TRACE_OUTPUT_DIR")
	envBool(&cfg.Debug.MaskKey, "TRAJECTA_MASK_KEY")
	envString(&cfg.ResponsesServer.DefaultModel, "TRAJECTA_RESPONSES_DEFAULT_MODEL")
	envBool(&cfg.ResponsesServer.ForceStore, "TRAJECTA_RESPONSES_FORCE_STORE")
	envInt64(&cfg.ResponsesServer.MaxRequestBodyBytes, "TRAJECTA_RESPONSES_MAX_REQUEST_BODY_BYTES")
	envString(&cfg.ResponsesServer.Path, "TRAJECTA_RESPONSES_PATH")
	envBool(&cfg.ResponsesServer.AutoCompact, "TRAJECTA_RESPONSES_AUTO_COMPACT")
	envInt(&cfg.ResponsesServer.CompactHistoryItemThreshold, "TRAJECTA_RESPONSES_COMPACT_HISTORY_ITEM_THRESHOLD")
	envBool(&cfg.ResponsesServer.FunctionExecutors.Enabled, "TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_ENABLED")
	envDuration(&cfg.ResponsesServer.FunctionExecutors.Timeout, "TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_TIMEOUT")
	envInt(&cfg.ResponsesServer.FunctionExecutors.MaxResultBytes, "TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_MAX_RESULT_BYTES")
	envBool(&cfg.ResponsesServer.FunctionExecutors.Redaction.Arguments, "TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_REDACT_ARGUMENTS")
	envBool(&cfg.ResponsesServer.FunctionExecutors.Redaction.Output, "TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_REDACT_OUTPUT")
	envBool(&cfg.ResponsesServer.CodexCompat.Enabled, "TRAJECTA_RESPONSES_CODEX_COMPAT_ENABLED")
	envList(&cfg.ResponsesServer.CodexCompat.AutoInjectHostedTools, "TRAJECTA_RESPONSES_CODEX_COMPAT_AUTO_INJECT_HOSTED_TOOLS")
	envBoolPtr(&cfg.ResponsesServer.CodexCompat.InjectWhenToolsAbsent, "TRAJECTA_RESPONSES_CODEX_COMPAT_INJECT_WHEN_TOOLS_ABSENT")
	envBoolPtr(&cfg.ResponsesServer.CodexCompat.PreserveClientTools, "TRAJECTA_RESPONSES_CODEX_COMPAT_PRESERVE_CLIENT_TOOLS")
	envAny(&cfg.ResponsesServer.CodexCompat.DefaultToolChoice, "TRAJECTA_RESPONSES_CODEX_COMPAT_DEFAULT_TOOL_CHOICE")
	envBool(&cfg.Tools.WebSearch.Enabled, "TRAJECTA_TOOLS_WEB_SEARCH_ENABLED")
	envString(&cfg.Tools.WebSearch.Provider, "TRAJECTA_TOOLS_WEB_SEARCH_PROVIDER")
	envInt(&cfg.Tools.WebSearch.MaxResults, "TRAJECTA_TOOLS_WEB_SEARCH_MAX_RESULTS")
	envString(&cfg.Tools.WebSearch.BaseURL, "TRAJECTA_TOOLS_WEB_SEARCH_BASE_URL")
	envInt(&cfg.Tools.WebSearch.TimeoutMS, "TRAJECTA_TOOLS_WEB_SEARCH_TIMEOUT_MS")
	envString(&cfg.Tools.WebSearch.UserAgent, "TRAJECTA_TOOLS_WEB_SEARCH_USER_AGENT")
	envBool(&cfg.Tools.MCP.Enabled, "TRAJECTA_TOOLS_MCP_ENABLED")
	envInt(&cfg.Tools.MCP.DefaultTimeoutMS, "TRAJECTA_TOOLS_MCP_DEFAULT_TIMEOUT_MS")
	envInt(&cfg.Tools.MCP.MaxResultBytes, "TRAJECTA_TOOLS_MCP_MAX_RESULT_BYTES")
}

// envString applies a non-empty value to a string field.
func envString(target *string, key string) {
	if v := os.Getenv(key); v != "" {
		*target = v
	}
}

// envAny applies a non-empty value to a field declared as any, which is how the
// optional Codex compatibility fields are typed.
func envAny(target *any, key string) {
	if v := os.Getenv(key); v != "" {
		*target = v
	}
}

// envStringPair applies one non-empty value to two string fields, which is how
// TRAJECTA_OUTPUT_DIR seeds both output directories before the more specific
// TRAJECTA_TRACE_OUTPUT_DIR overrides one of them.
func envStringPair(first, second *string, key string) {
	if v := os.Getenv(key); v != "" {
		*first = v
		*second = v
	}
}

// envBool applies a value that must parse as a bool; one that does not is ignored,
// leaving the loaded value in place.
func envBool(target *bool, key string) {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			*target = parsed
		}
	}
}

// envBoolPtr is envBool for an optional field, where unset and false differ. Each
// call owns its parsed value, so no two fields share a pointer.
func envBoolPtr(target **bool, key string) {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			*target = &parsed
		}
	}
}

// envInt applies a value that must parse as an int.
func envInt(target *int, key string) {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			*target = parsed
		}
	}
}

// envInt64 applies a value that must parse as a 64-bit int.
func envInt64(target *int64, key string) {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			*target = parsed
		}
	}
}

// envDuration applies a value that must parse as a Go duration.
func envDuration(target *time.Duration, key string) {
	if v := os.Getenv(key); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			*target = parsed
		}
	}
}

// envList applies a comma-separated list.
func envList(target *[]string, key string) {
	if v := os.Getenv(key); v != "" {
		*target = splitCommaEnv(v)
	}
}

// envUpstream applies a value to the single-upstream configuration and to the
// first entry of the upstream list, which is what the variables did as blocks: the
// configuration carries both shapes, and an entry only exists once the list does.
func envUpstream(cfg *Config, key string, assign func(*UpstreamConfig, string)) {
	if v := os.Getenv(key); v != "" {
		assign(&cfg.Upstream, v)
		applyFirstUpstreamOverride(cfg, func(upstream *UpstreamConfig) {
			assign(upstream, v)
		})
	}
}

// envBootstrapUpstream is envUpstream for the variables that create the bootstrap
// entry first, and only when no upstream is configured.
func envBootstrapUpstream(cfg *Config, key string, assign func(*UpstreamConfig, string)) {
	if v := os.Getenv(key); v != "" {
		ensureBootstrapUpstream(cfg)
		applyFirstUpstreamOverrideOrSingle(cfg, func(upstream *UpstreamConfig) {
			assign(upstream, v)
		})
	}
}

func applyFirstUpstreamOverride(cfg *Config, apply func(*UpstreamConfig)) {
	if len(cfg.Upstreams) == 0 {
		return
	}
	apply(&cfg.Upstreams[0].Upstream)
}

func applyFirstUpstreamOverrideOrSingle(cfg *Config, apply func(*UpstreamConfig)) {
	if len(cfg.Upstreams) > 0 {
		apply(&cfg.Upstreams[0].Upstream)
		return
	}
	apply(&cfg.Upstream)
}

func ensureBootstrapUpstream(cfg *Config) {
	if cfg == nil || len(cfg.Upstreams) > 0 || strings.TrimSpace(cfg.Upstream.BaseURL) != "" {
		return
	}
	chatCompletions := true
	responses := false
	toolCalling := true
	models := true
	tokenize := false
	cfg.Upstream = UpstreamConfig{
		ProviderPreset: "openai",
		APIType:        "chat_completions",
		Mode:           "proxy",
		Capabilities: UpstreamCapabilitiesConfig{
			ChatCompletions: &chatCompletions,
			Responses:       &responses,
			ToolCalling:     &toolCalling,
			Models:          &models,
			Tokenize:        &tokenize,
		},
		ProtocolFamily: "openai_compatible",
		// Must be a routing profile registered in internal/upstream; the bare
		// "openai" value is only a provider preset name and would be rejected
		// by upstream.Resolve.
		RoutingProfile: "openai_default",
		Headers:        map[string]string{},
	}
}

func expandEnvRefs(target any) error {
	return expandEnvValue(reflect.ValueOf(target), "")
}

func expandEnvValue(value reflect.Value, path string) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return expandEnvValue(value.Elem(), path)
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			fieldType := value.Type().Field(i)
			if fieldType.PkgPath != "" {
				continue
			}
			nextPath := fieldType.Name
			if path != "" {
				nextPath = path + "." + nextPath
			}
			if err := expandEnvValue(field, nextPath); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if err := expandEnvValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			mapValue := value.MapIndex(key)
			if mapValue.Kind() == reflect.String {
				expanded, err := expandEnvString(mapValue.String(), fmt.Sprintf("%s[%s]", path, key.String()))
				if err != nil {
					return err
				}
				value.SetMapIndex(key, reflect.ValueOf(expanded))
				continue
			}
			copyValue := reflect.New(mapValue.Type()).Elem()
			copyValue.Set(mapValue)
			if err := expandEnvValue(copyValue, fmt.Sprintf("%s[%s]", path, key.String())); err != nil {
				return err
			}
			value.SetMapIndex(key, copyValue)
		}
	case reflect.String:
		if !value.CanSet() {
			return nil
		}
		expanded, err := expandEnvString(value.String(), path)
		if err != nil {
			return err
		}
		value.SetString(expanded)
	}
	return nil
}

func expandEnvString(raw string, path string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "$env:") {
		return raw, nil
	}
	name := strings.TrimSpace(strings.TrimPrefix(trimmed, "$env:"))
	if name == "" {
		return "", fmt.Errorf("empty env reference at %s", path)
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		if optionalMissingEnvRef(name, path) {
			return "", nil
		}
		return "", fmt.Errorf("environment variable %s referenced at %s is not set", name, path)
	}
	return value, nil
}

func optionalMissingEnvRef(name string, path string) bool {
	if name != "LLM_API_KEY" {
		return false
	}
	return path == "Upstream.ApiKey" || strings.HasSuffix(path, ".Upstream.ApiKey")
}

func (c Config) EffectiveUpstreams() []UpstreamTargetConfig {
	if len(c.Upstreams) > 0 {
		return append([]UpstreamTargetConfig(nil), c.Upstreams...)
	}
	if strings.TrimSpace(c.Upstream.BaseURL) == "" {
		return nil
	}

	enabled := true
	return []UpstreamTargetConfig{
		{
			ID:       "default",
			Enabled:  &enabled,
			Priority: 100,
			Weight:   1,
			Upstream: c.Upstream,
		},
	}
}

func (t UpstreamTargetConfig) HasExplicitCredentials() bool {
	return len(t.Credentials) > 0
}

func (t UpstreamTargetConfig) EffectiveCredentials() []CredentialConfig {
	if len(t.Credentials) > 0 {
		return cloneCredentialConfigs(t.Credentials)
	}
	if strings.TrimSpace(t.Upstream.ApiKey) == "" {
		return nil
	}
	enabled := true
	return []CredentialConfig{
		{
			ID:      "default",
			Name:    "default",
			Enabled: &enabled,
			ApiKey:  t.Upstream.ApiKey,
		},
	}
}

func cloneCredentialConfigs(in []CredentialConfig) []CredentialConfig {
	out := make([]CredentialConfig, len(in))
	for i, credential := range in {
		out[i] = credential
		if credential.Headers != nil {
			out[i].Headers = make(map[string]string, len(credential.Headers))
			for key, value := range credential.Headers {
				out[i].Headers[key] = value
			}
		}
	}
	return out
}

func (c Config) AuthSessionTTL() time.Duration {
	if c.Auth.SessionTTL > 0 {
		return c.Auth.SessionTTL
	}
	return 24 * time.Hour
}

// ServerReadTimeout bounds reading one request, including its body.
func (c Config) ServerReadTimeout() time.Duration {
	if c.Server.ReadTimeout > 0 {
		return c.Server.ReadTimeout
	}
	return 5 * time.Minute
}

// UpstreamResponseHeaderTimeout is how long a forwarded request waits for the
// upstream to start answering - the transport's ResponseHeaderTimeout, counted
// from the end of the request write until the response headers arrive. Zero
// means no bound, which is the default.
//
// The default is zero because this proxy cannot tell a stuck upstream from a
// slow one: a non-streaming reasoning call can legitimately spend minutes
// before its first byte, and the bounds the transport already has (dial, TLS
// handshake, idle connection) are all blind to that wait. The cost of leaving it
// unbounded is that an upstream which accepts the connection and then never
// answers holds the client's request, its concurrency slot and its connection
// until the client gives up. A deployment that would rather fail fast than hold
// those resources sets `server.upstream_response_header_timeout` or
// `TRAJECTA_SERVER_UPSTREAM_RESPONSE_HEADER_TIMEOUT`; for a streaming call the
// value only has to cover the time to first byte, not the time to the last
// token, because the headers arrive before the body does.
func (c Config) UpstreamResponseHeaderTimeout() time.Duration {
	return c.Server.UpstreamResponseHeaderTimeout
}

// ServerWriteTimeout is the server-side deadline for writing one response. Zero
// means no deadline, which is the default.
//
// `http.Server.WriteTimeout` covers the whole response write, not the gap
// between writes, so a fixed value truncates every response that legitimately
// runs longer. That is the normal case here: the proxy forwards chat
// completions and the local Responses runtime streams them, and neither has a
// natural upper bound — a long reasoning or code-generation turn routinely
// exceeds the five minutes this used to be capped at, and the client saw the
// connection close mid-body with no protocol error it could interpret. What
// still bounds a request is the caller: the outbound upstream request carries
// the incoming request context, so a cancelled SDK call cancels the upstream
// call, and the transport's dial and TLS timeouts bound a connection that never
// establishes. Operators that want a hard cap (a public deployment protecting
// against a client that reads slowly) can set `server.write_timeout` or
// `TRAJECTA_SERVER_WRITE_TIMEOUT`.
func (c Config) ServerWriteTimeout() time.Duration {
	if c.Server.WriteTimeout > 0 {
		return c.Server.WriteTimeout
	}
	return 0
}

func (c Config) TraceOutputDir() string {
	if strings.TrimSpace(c.Trace.OutputDir) != "" {
		return c.Trace.OutputDir
	}
	return c.Debug.OutputDir
}

// DatabaseDriver returns the configured application database driver. Postgres
// is the default: the application database is the shared source of truth, so a
// configuration that does not name a driver must not silently create a local
// SQLite file. SQLite remains available for the local test harness and for
// reading legacy databases during migration, but only when it is named
// explicitly.
func (c Config) DatabaseDriver() string {
	if strings.TrimSpace(c.Database.Driver) != "" {
		return strings.ToLower(strings.TrimSpace(c.Database.Driver))
	}
	return "postgres"
}

// DefaultSQLiteFileName is the local SQLite application database file used when
// neither database.dsn nor auth.database_path is configured.
const DefaultSQLiteFileName = "trajecta.sqlite3"

// LegacySQLiteFileName is the pre-rename default file name. A local database
// created before the project was renamed from llm-tracelab is opened in place
// instead of silently starting a new empty one.
const LegacySQLiteFileName = "llm_tracelab.sqlite3"

// DefaultSQLitePath returns the default local SQLite database path inside
// outputDir.
func DefaultSQLitePath(outputDir string) string {
	return filepath.Join(outputDir, DefaultSQLiteFileName)
}

// ResolveDefaultSQLitePath prefers DefaultSQLitePath(outputDir), but falls back
// to an existing legacy llm_tracelab.sqlite3 file so that a local database
// created before the rename keeps working without manual migration.
func ResolveDefaultSQLitePath(outputDir string) string {
	path := DefaultSQLitePath(outputDir)
	if _, err := os.Stat(path); err == nil {
		return path
	}
	legacy := filepath.Join(outputDir, LegacySQLiteFileName)
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return path
}

func (c Config) DatabasePath() string {
	if strings.TrimSpace(c.Database.DSN) != "" && c.DatabaseDriver() == "sqlite" {
		if path := SQLitePathFromDSN(c.Database.DSN); path != "" {
			return path
		}
	}
	if strings.TrimSpace(c.Auth.DatabasePath) != "" {
		return c.Auth.DatabasePath
	}
	return ResolveDefaultSQLitePath(c.TraceOutputDir())
}

func SQLitePathFromDSN(dsn string) string {
	path := strings.TrimSpace(dsn)
	if path == "" {
		return ""
	}
	path = strings.TrimPrefix(path, "sqlite://")
	path = strings.TrimPrefix(path, "file:")
	if idx := strings.Index(path, "?"); idx >= 0 {
		path = path[:idx]
	}
	return strings.TrimSpace(path)
}

func (c Config) DatabaseDSN() string {
	if strings.TrimSpace(c.Database.DSN) != "" {
		return c.Database.DSN
	}
	if c.DatabaseDriver() == "sqlite" {
		return c.DatabasePath()
	}
	return ""
}

const redactedDSNValue = "<redacted>"

// RedactDSN hides the credential in a database DSN before it is printed or
// logged. Two shapes carry one: URL DSNs put it in the userinfo or in a query
// parameter (lib/pq and the MySQL driver both accept keyword parameters as URL
// query values, so `?password=` is a working configuration), and keyword DSNs
// put it in a space-separated `key=value` field.
//
// Both the URL query keys and the keyword field keys are matched with
// redaction.IsSensitiveURLParam so that this shares one marker list with the
// URL redaction used by the recorder and the proxy logs. Matching only the
// literal `password=`/`passwd=` spelling of one shape left the same secret
// visible whenever it was spelled differently.
func RedactDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return ""
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") || strings.HasPrefix(dsn, "mysql://") {
		return redactURLDSN(dsn)
	}
	parts := strings.Fields(dsn)
	changed := false
	for i, part := range parts {
		key, _, hasValue := strings.Cut(part, "=")
		if !hasValue || !redaction.IsSensitiveURLParam(key) {
			continue
		}
		parts[i] = key + "=" + redactedDSNValue
		changed = true
	}
	if !changed {
		return dsn
	}
	return strings.Join(parts, " ")
}

// redactURLDSN redacts the userinfo password and every sensitive query
// parameter of a URL DSN. The query is rewritten textually rather than through
// url.Values so that a redacted DSN keeps the original parameter order and
// escapes instead of being re-encoded.
func redactURLDSN(dsn string) string {
	redacted := redactURLPassword(dsn)
	base, rawQuery, hasQuery := strings.Cut(redacted, "?")
	if !hasQuery || rawQuery == "" {
		return redacted
	}
	params := strings.Split(rawQuery, "&")
	changed := false
	for i, param := range params {
		key, _, hasValue := strings.Cut(param, "=")
		if !hasValue {
			continue
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		if !redaction.IsSensitiveURLParam(decodedKey) {
			continue
		}
		params[i] = key + "=" + redactedDSNValue
		changed = true
	}
	if !changed {
		return redacted
	}
	return base + "?" + strings.Join(params, "&")
}

func redactURLPassword(dsn string) string {
	parts := strings.SplitN(dsn, "://", 2)
	if len(parts) != 2 {
		return dsn
	}
	scheme, rest := parts[0], parts[1]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return dsn
	}
	userInfo := rest[:at]
	if !strings.Contains(userInfo, ":") {
		return dsn
	}
	user, _, _ := strings.Cut(userInfo, ":")
	return scheme + "://" + user + ":<redacted>@" + rest[at+1:]
}

func (c Config) DatabaseAutoMigrate() bool {
	if c.Database.AutoMigrate != nil {
		return *c.Database.AutoMigrate
	}
	return true
}

func (c Config) DatabaseUseSessionSummaryRead() bool {
	if c.Database.UseSessionSummaryRead != nil {
		return *c.Database.UseSessionSummaryRead
	}
	return false
}

func (c Config) DatabaseMaxOpenConns() int {
	if c.Database.MaxOpenConns > 0 {
		return c.Database.MaxOpenConns
	}
	return 4
}

func (c Config) DatabaseMaxIdleConns() int {
	if c.Database.MaxIdleConns > 0 {
		return c.Database.MaxIdleConns
	}
	return 4
}

func (c Config) ProviderProbeStartupFillEnabled() bool {
	return c.ProviderProbe.StartupFill
}

func (c Config) ProviderProbeTimeout() time.Duration {
	if c.ProviderProbe.Timeout > 0 {
		return c.ProviderProbe.Timeout
	}
	return 10 * time.Second
}

func (c Config) ResponsesDefaultModel() string {
	return strings.TrimSpace(c.ResponsesServer.DefaultModel)
}

func (c Config) ResponsesForceStore() bool {
	return c.ResponsesServer.ForceStore
}

func (c Config) ResponsesMaxRequestBodyBytes() int64 {
	if c.ResponsesServer.MaxRequestBodyBytes > 0 {
		return c.ResponsesServer.MaxRequestBodyBytes
	}
	// Responses requests may contain base64-encoded image inputs. Keep the
	// built-in limit high enough for those requests while retaining a finite
	// guard against unbounded bodies.
	return 64 << 20
}

func (c Config) ResponsesServerPath() string {
	if path := strings.TrimSpace(c.ResponsesServer.Path); path != "" {
		return path
	}
	return "/v1/responses"
}

func (c Config) ResponsesAutoCompactEnabled() bool {
	return c.ResponsesServer.AutoCompact
}

func (c Config) ResponsesCompactHistoryItemThreshold() int {
	if c.ResponsesServer.CompactHistoryItemThreshold > 0 {
		return c.ResponsesServer.CompactHistoryItemThreshold
	}
	return 0
}

func (c Config) ResponsesModelProfiles() []ResponsesModelProfileConfig {
	profiles := make([]ResponsesModelProfileConfig, 0, len(c.ResponsesServer.ModelProfiles))
	for _, profile := range c.ResponsesServer.ModelProfiles {
		profile.Name = strings.TrimSpace(profile.Name)
		profile.Pattern = strings.TrimSpace(profile.Pattern)
		profile.ModelReasoningEffort = strings.TrimSpace(profile.ModelReasoningEffort)
		profile.UpstreamModel = strings.TrimSpace(profile.UpstreamModel)
		profile.TokenizeCounter.UpstreamID = strings.TrimSpace(profile.TokenizeCounter.UpstreamID)
		profiles = append(profiles, profile)
	}
	return profiles
}

func (c Config) ResponsesAdoptChannelModelProfilesEnabled() bool {
	return c.ResponsesServer.AdoptChannelModelProfiles
}

func (c Config) ResponsesCodexCompatConfig() ResponsesCodexCompatConfig {
	cfg := c.ResponsesServer.CodexCompat
	cfg.AutoInjectHostedTools = trimStringSlice(cfg.AutoInjectHostedTools)
	if cfg.InjectWhenToolsAbsent == nil {
		value := true
		cfg.InjectWhenToolsAbsent = &value
	}
	if cfg.PreserveClientTools == nil {
		value := true
		cfg.PreserveClientTools = &value
	}
	if cfg.DefaultToolChoice == nil {
		cfg.DefaultToolChoice = "auto"
	} else if value, ok := cfg.DefaultToolChoice.(string); ok {
		value = strings.TrimSpace(value)
		if value == "" {
			value = "auto"
		}
		cfg.DefaultToolChoice = value
	}
	return cfg
}

func (c Config) MatchResponsesModelProfile(model string) ResponsesModelProfileMatch {
	model = strings.TrimSpace(model)
	if model == "" {
		return ResponsesModelProfileMatch{Index: -1, Source: "none"}
	}
	profiles := c.ResponsesModelProfiles()
	for idx, profile := range profiles {
		if profile.Name == "" || profile.Name != model {
			continue
		}
		return ResponsesModelProfileMatch{
			Matched: true,
			Index:   idx,
			Kind:    "exact",
			Source:  fmt.Sprintf("responses_server.model_profiles[%d].name", idx),
			Profile: profile,
		}
	}
	for idx, profile := range profiles {
		if !wildcardMatchString(profile.Pattern, model) {
			continue
		}
		return ResponsesModelProfileMatch{
			Matched: true,
			Index:   idx,
			Kind:    "pattern",
			Source:  fmt.Sprintf("responses_server.model_profiles[%d].pattern", idx),
			Profile: profile,
		}
	}
	return ResponsesModelProfileMatch{Index: -1, Source: "none"}
}

func wildcardMatchString(pattern string, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	pi, vi := 0, 0
	star, match := -1, 0
	for vi < len(value) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == value[vi]) {
			pi++
			vi++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			match = vi
			pi++
			continue
		}
		if star != -1 {
			pi = star + 1
			match++
			vi = match
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

func (c Config) ResponsesFunctionExecutorsConfig() ResponsesFunctionExecutorConfig {
	cfg := c.ResponsesServer.FunctionExecutors
	cfg.Warnings = nil
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxResultBytes <= 0 {
		cfg.MaxResultBytes = 64 << 10
	}
	bindings := make([]ResponsesFunctionExecutorBinding, 0, len(cfg.Executors))
	seen := map[string]struct{}{}
	enabledAvailable := 0
	for _, binding := range cfg.Executors {
		binding.Name = strings.TrimSpace(binding.Name)
		binding.Type = strings.ToLower(strings.TrimSpace(binding.Type))
		binding.Command = strings.TrimSpace(binding.Command)
		binding.Process.WorkingDir = strings.TrimSpace(binding.Process.WorkingDir)
		binding.Process.AllowedCommandDirs = trimStringSlice(binding.Process.AllowedCommandDirs)
		binding.Available = false
		binding.Warnings = nil
		for i := range binding.EnvAllowlist {
			binding.EnvAllowlist[i] = strings.TrimSpace(binding.EnvAllowlist[i])
		}
		enabled := true
		if binding.Enabled != nil {
			enabled = *binding.Enabled
		}
		if binding.Name == "" {
			binding.Warnings = append(binding.Warnings, "executor name is required")
		} else if _, ok := seen[binding.Name]; ok {
			binding.Warnings = append(binding.Warnings, fmt.Sprintf("duplicate executor name %q is ignored", binding.Name))
		} else {
			seen[binding.Name] = struct{}{}
		}
		switch binding.Type {
		case ResponsesFunctionExecutorTypeStaticResponse:
			if len(binding.Warnings) == 0 && enabled {
				binding.Available = true
				enabledAvailable++
			}
		case ResponsesFunctionExecutorTypeExternalCommand:
			if binding.Command == "" {
				binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q command is required", binding.Name))
			}
			if binding.Process.WorkingDir != "" {
				if !filepath.IsAbs(binding.Process.WorkingDir) {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.working_dir must be absolute", binding.Name))
				} else if info, err := os.Stat(binding.Process.WorkingDir); err != nil {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.working_dir is not accessible: %v", binding.Name, err))
				} else if !info.IsDir() {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.working_dir must be a directory", binding.Name))
				}
			}
			if binding.Process.RequireAbsoluteCommand && binding.Command != "" && !filepath.IsAbs(binding.Command) {
				binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q command must be absolute when process.require_absolute_command is true", binding.Name))
			}
			allowedCommandDirs := make([]string, 0, len(binding.Process.AllowedCommandDirs))
			for _, dir := range binding.Process.AllowedCommandDirs {
				if dir == "" {
					continue
				}
				if !filepath.IsAbs(dir) {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.allowed_command_dirs entries must be absolute", binding.Name))
					continue
				}
				info, err := os.Stat(dir)
				if err != nil {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.allowed_command_dirs entry is not accessible: %v", binding.Name, err))
					continue
				}
				if !info.IsDir() {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.allowed_command_dirs entries must be directories", binding.Name))
					continue
				}
				resolved, err := filepath.EvalSymlinks(dir)
				if err != nil {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.allowed_command_dirs entry cannot resolve symlinks: %v", binding.Name, err))
					continue
				}
				if binding.Process.RejectRoot && filepath.Clean(resolved) == string(filepath.Separator) {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q process.allowed_command_dirs must not include filesystem root when process.reject_root is true", binding.Name))
					continue
				}
				allowedCommandDirs = append(allowedCommandDirs, resolved)
			}
			if binding.Command != "" && len(allowedCommandDirs) > 0 {
				if !filepath.IsAbs(binding.Command) {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q command must be absolute when process.allowed_command_dirs is configured", binding.Name))
				} else if resolvedCommand, err := filepath.EvalSymlinks(binding.Command); err != nil {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q command cannot resolve symlinks: %v", binding.Name, err))
				} else if !pathWithinAnyDir(resolvedCommand, allowedCommandDirs) {
					binding.Warnings = append(binding.Warnings, fmt.Sprintf("external_command executor %q command must resolve inside process.allowed_command_dirs", binding.Name))
				}
			}
			if len(binding.Warnings) == 0 && enabled {
				binding.Available = true
				enabledAvailable++
			}
		default:
			if binding.Type == "" {
				binding.Warnings = append(binding.Warnings, fmt.Sprintf("executor %q type is required", binding.Name))
			} else {
				binding.Warnings = append(binding.Warnings, fmt.Sprintf("unsupported executor type %q for %q", binding.Type, binding.Name))
			}
		}
		cfg.Warnings = append(cfg.Warnings, binding.Warnings...)
		bindings = append(bindings, binding)
	}
	if cfg.Enabled && len(bindings) == 0 {
		cfg.Warnings = append(cfg.Warnings, "responses function executors are enabled but no executors are configured")
	} else if cfg.Enabled && enabledAvailable == 0 {
		cfg.Warnings = append(cfg.Warnings, "responses function executors are enabled but no available executors are configured")
	}
	if cfg.Warnings == nil {
		cfg.Warnings = []string{}
	}
	cfg.Executors = bindings
	return cfg
}

func trimStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func splitCommaEnv(value string) []string {
	return trimStringSlice(strings.Split(value, ","))
}

func pathWithinAnyDir(path string, dirs []string) bool {
	for _, dir := range dirs {
		if pathWithinDir(path, dir) {
			return true
		}
	}
	return false
}

func pathWithinDir(path string, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (c Config) WebSearchEnabled() bool {
	return c.WebSearchConfig().Enabled
}

func (c Config) WebSearchConfig() WebSearchToolConfig {
	cfg := c.Tools.WebSearch
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	if cfg.Provider == "" {
		cfg.Provider = "disabled"
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 5
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = 5000
	}
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	if cfg.UserAgent == "" {
		cfg.UserAgent = "trajecta web_search"
	}
	return cfg
}

func (c Config) MCPToolsEnabled() bool {
	return c.MCPToolsConfig().Enabled
}

func (c Config) MCPToolsConfig() MCPToolConfig {
	cfg := c.Tools.MCP
	if cfg.DefaultTimeoutMS <= 0 {
		cfg.DefaultTimeoutMS = 60000
	}
	if cfg.MaxResultBytes <= 0 {
		cfg.MaxResultBytes = 65536
	}
	for i := range cfg.Servers {
		cfg.Servers[i].ID = strings.TrimSpace(cfg.Servers[i].ID)
		cfg.Servers[i].Label = strings.TrimSpace(cfg.Servers[i].Label)
		cfg.Servers[i].URL = strings.TrimSpace(cfg.Servers[i].URL)
		cfg.Servers[i].BearerTokenEnv = strings.TrimSpace(cfg.Servers[i].BearerTokenEnv)
		cfg.Servers[i].EnabledTools = trimStringSlice(cfg.Servers[i].EnabledTools)
		cfg.Servers[i].DisabledTools = trimStringSlice(cfg.Servers[i].DisabledTools)
	}
	return cfg
}
