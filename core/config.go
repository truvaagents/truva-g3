package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration options for the Truva-G3 framework.
// It supports three-layer configuration priority:
//  1. Default values (lowest priority)
//  2. Environment variables (medium priority)
//  3. Functional options (highest priority)
//
// The configuration automatically detects the execution environment (Kubernetes vs local)
// and adjusts defaults accordingly.
//
// Example usage:
//
//	cfg, err := NewConfig(
//	    WithName("my-agent"),
//	    WithPort(8080),
//	    WithCORS([]string{"https://example.com"}, true),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
type Config struct {
	// Core configuration
	Name      string `json:"name" env:"TRUVAG3_AGENT_NAME"`
	ID        string `json:"id" env:"TRUVAG3_AGENT_ID"`
	Port      int    `json:"port" env:"TRUVAG3_PORT" default:"8080"`
	Address   string `json:"address" env:"TRUVAG3_ADDRESS"`
	Namespace string `json:"namespace" env:"TRUVAG3_NAMESPACE" default:"default"`

	// HTTP Server configuration
	HTTP HTTPConfig `json:"http"`

	// Discovery configuration
	Discovery DiscoveryConfig `json:"discovery"`

	// AI configuration (optional module)
	AI AIConfig `json:"ai"`

	// Telemetry configuration (optional module)
	Telemetry TelemetryConfig `json:"telemetry"`

	// Memory configuration
	Memory MemoryConfig `json:"memory"`

	// Shared memory configuration (cross-agent coordination)
	SharedMemory SharedMemoryConfig `json:"shared_memory"`

	// Activity coordination configuration (real-time agent signals)
	ActivityCoordination ActivityCoordinationConfig `json:"activity_coordination"`

	// Resilience configuration
	Resilience ResilienceConfig `json:"resilience"`

	// Logging configuration
	Logging LoggingConfig `json:"logging"`

	// Development configuration
	Development DevelopmentConfig `json:"development"`

	// Kubernetes specific configuration
	Kubernetes KubernetesConfig `json:"kubernetes"`

	// Logger instance for configuration operations (excluded from JSON)
	logger Logger `json:"-"`
}

// HTTPConfig contains HTTP server configuration including timeouts, limits, and CORS settings.
// All timeout values use time.Duration for flexibility.
type HTTPConfig struct {
	ReadTimeout       time.Duration `json:"read_timeout" env:"TRUVAG3_HTTP_READ_TIMEOUT" default:"30s"`
	ReadHeaderTimeout time.Duration `json:"read_header_timeout" env:"TRUVAG3_HTTP_READ_HEADER_TIMEOUT" default:"10s"`
	WriteTimeout      time.Duration `json:"write_timeout" env:"TRUVAG3_HTTP_WRITE_TIMEOUT" default:"30s"`
	IdleTimeout       time.Duration `json:"idle_timeout" env:"TRUVAG3_HTTP_IDLE_TIMEOUT" default:"120s"`
	MaxHeaderBytes    int           `json:"max_header_bytes" env:"TRUVAG3_HTTP_MAX_HEADER_BYTES" default:"1048576"`
	ShutdownTimeout   time.Duration `json:"shutdown_timeout" env:"TRUVAG3_HTTP_SHUTDOWN_TIMEOUT" default:"10s"`
	EnableHealthCheck bool          `json:"enable_health_check" env:"TRUVAG3_HTTP_HEALTH_CHECK" default:"true"`
	HealthCheckPath   string        `json:"health_check_path" env:"TRUVAG3_HTTP_HEALTH_PATH" default:"/health"`

	// EnableOpenAPI controls whether the auto-generated GET /openapi.json
	// endpoint is exposed on this component.
	//
	// Default: false (opt-in). Disabling is the safe default because the
	// endpoint reveals every registered capability and its schema to any
	// caller that can reach the pod. Leaving it on in production would
	// leak the component's full API surface.
	//
	// Enable for development and staging via WithOpenAPI(true) or the
	// TRUVAG3_ENABLE_OPENAPI=true environment variable. In production,
	// leave it unset (or explicitly set to false).
	EnableOpenAPI bool `json:"enable_openapi" env:"TRUVAG3_ENABLE_OPENAPI" default:"false"`

	CORS CORSConfig `json:"cors"`

	// Middleware is a list of custom middleware functions to apply to the HTTP handler.
	// These are applied in order, with the first middleware being the outermost.
	// This allows applications to inject telemetry middleware (e.g., tracing) without
	// core importing telemetry - following the framework's modular architecture.
	//
	// Example usage:
	//   core.WithMiddleware(telemetry.TracingMiddleware("my-service"))
	//
	// Note: This field is excluded from JSON serialization as middleware functions
	// cannot be serialized.
	Middleware []func(http.Handler) http.Handler `json:"-"`
}

// CORSConfig contains Cross-Origin Resource Sharing (CORS) configuration.
// Supports wildcard domains (e.g., *.example.com) and wildcard ports (e.g., http://localhost:*).
//
// Security note: Be cautious with AllowCredentials=true and ensure AllowedOrigins
// is properly restricted in production environments.
type CORSConfig struct {
	Enabled          bool     `json:"enabled" env:"TRUVAG3_CORS_ENABLED" default:"false"`
	AllowedOrigins   []string `json:"allowed_origins" env:"TRUVAG3_CORS_ORIGINS"`
	AllowedMethods   []string `json:"allowed_methods" env:"TRUVAG3_CORS_METHODS" default:"GET,POST,PUT,DELETE,OPTIONS"`
	AllowedHeaders   []string `json:"allowed_headers" env:"TRUVAG3_CORS_HEADERS" default:"Content-Type,Authorization"`
	ExposedHeaders   []string `json:"exposed_headers" env:"TRUVAG3_CORS_EXPOSED_HEADERS"`
	AllowCredentials bool     `json:"allow_credentials" env:"TRUVAG3_CORS_CREDENTIALS" default:"false"`
	MaxAge           int      `json:"max_age" env:"TRUVAG3_CORS_MAX_AGE" default:"86400"`
}

// DiscoveryConfig contains service discovery configuration.
// Currently supports Redis as the discovery backend with optional caching.
// When MockDiscovery is enabled in Development mode, an in-memory discovery is used instead.
type DiscoveryConfig struct {
	Enabled           bool          `json:"enabled" env:"TRUVAG3_DISCOVERY_ENABLED" default:"false"`
	Provider          string        `json:"provider" env:"TRUVAG3_DISCOVERY_PROVIDER" default:"redis"`
	RedisURL          string        `json:"redis_url" env:"TRUVAG3_REDIS_URL,REDIS_URL"`
	CacheEnabled      bool          `json:"cache_enabled" env:"TRUVAG3_DISCOVERY_CACHE" default:"true"`
	CacheTTL          time.Duration `json:"cache_ttl" env:"TRUVAG3_DISCOVERY_CACHE_TTL" default:"5m"`
	HeartbeatInterval time.Duration `json:"heartbeat_interval" env:"TRUVAG3_DISCOVERY_HEARTBEAT" default:"0"`
	TTL               time.Duration `json:"ttl" env:"TRUVAG3_DISCOVERY_TTL" default:"30s"`

	// Retry configuration for handling initial connection failures
	RetryOnFailure bool          `json:"retry_on_failure" env:"TRUVAG3_DISCOVERY_RETRY" default:"false"`
	RetryInterval  time.Duration `json:"retry_interval" env:"TRUVAG3_DISCOVERY_RETRY_INTERVAL" default:"30s"`
}

// AIConfig contains AI client configuration for LLM integration.
// This is an optional module - AI features are only initialized when Enabled=true.
// Supports OpenAI and compatible APIs. When MockAI is enabled in Development mode,
// returns canned responses without making actual API calls.
type AIConfig struct {
	Enabled       bool          `json:"enabled" env:"TRUVAG3_AI_ENABLED" default:"false"`
	Provider      string        `json:"provider" env:"TRUVAG3_AI_PROVIDER" default:"openai"`
	APIKey        string        `json:"api_key" env:"TRUVAG3_AI_API_KEY,OPENAI_API_KEY"`
	BaseURL       string        `json:"base_url" env:"TRUVAG3_AI_BASE_URL"`
	Model         string        `json:"model" env:"TRUVAG3_AI_MODEL" default:"gpt-4"`
	Temperature   float32       `json:"temperature" env:"TRUVAG3_AI_TEMPERATURE" default:"0.7"`
	MaxTokens     int           `json:"max_tokens" env:"TRUVAG3_AI_MAX_TOKENS" default:"2000"`
	Timeout       time.Duration `json:"timeout" env:"TRUVAG3_AI_TIMEOUT" default:"180s"`
	RetryAttempts int           `json:"retry_attempts" env:"TRUVAG3_AI_RETRY_ATTEMPTS" default:"3"`
	RetryDelay    time.Duration `json:"retry_delay" env:"TRUVAG3_AI_RETRY_DELAY" default:"1s"`
}

// TelemetryConfig contains observability configuration for metrics and distributed tracing.
// This is an optional module - telemetry is only initialized when Enabled=true.
// Supports OpenTelemetry (OTEL) protocol. The endpoint should be the OTLP receiver address.
type TelemetryConfig struct {
	Enabled        bool   `json:"enabled" env:"TRUVAG3_TELEMETRY_ENABLED" default:"false"`
	Provider       string `json:"provider" env:"TRUVAG3_TELEMETRY_PROVIDER" default:"otel"`
	Endpoint       string `json:"endpoint" env:"TRUVAG3_TELEMETRY_ENDPOINT,OTEL_EXPORTER_OTLP_ENDPOINT"`
	ServiceName    string `json:"service_name" env:"TRUVAG3_TELEMETRY_SERVICE_NAME,OTEL_SERVICE_NAME"`
	MetricsEnabled bool   `json:"metrics_enabled" env:"TRUVAG3_TELEMETRY_METRICS" default:"true"`
	TracingEnabled bool   `json:"tracing_enabled" env:"TRUVAG3_TELEMETRY_TRACING" default:"true"`
	Insecure       bool   `json:"insecure" env:"TRUVAG3_TELEMETRY_INSECURE" default:"true"`
}

// MemoryConfig contains state storage configuration for *MemoryStore.
// MaxSize and DefaultTTL are reserved for future use; CleanupInterval is
// consumed by Framework.AutoRegisterMemorySweeper() (or by application/tool
// code that constructs a sweeper directly via core.NewMemoryStoreSweeper).
// BaseAgent.Initialize does NOT touch Memory — sweeper lifecycle is the
// framework's job per FRAMEWORK_DESIGN_PRINCIPLES.md §"Background Jobs".
type MemoryConfig struct {
	MaxSize         int           `json:"max_size" env:"TRUVAG3_MEMORY_MAX_SIZE" default:"1000"`
	DefaultTTL      time.Duration `json:"default_ttl" env:"TRUVAG3_MEMORY_DEFAULT_TTL" default:"1h"`
	CleanupInterval time.Duration `json:"cleanup_interval" env:"TRUVAG3_MEMORY_CLEANUP_INTERVAL" default:"10m"`
}

// SharedMemoryConfig contains cross-agent shared memory configuration.
// Controls episodic memory, investigation coordination, and knowledge retrieval.
// All fields have sensible defaults — shared memory works with zero configuration
// when a Valkey/Redis instance is available.
type SharedMemoryConfig struct {
	// Provider selection — follows the same pattern as Discovery.Provider
	// "redis" uses existing Valkey/Redis, "noop" explicitly disables
	Provider string `json:"provider" env:"TRUVAG3_SHARED_MEMORY_PROVIDER" default:"noop"`

	// Redis/Valkey connection — reuses existing REDIS_URL / TRUVAG3_REDIS_URL
	// to avoid duplicate env vars per FRAMEWORK_DESIGN_PRINCIPLES §env var naming.
	// Only used when Provider is "redis".
	RedisURL string `json:"redis_url"`

	// Event stream max length (approximate, uses Redis ~MAXLEN for efficiency)
	StreamMaxLen int64 `json:"stream_max_len" env:"TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN" default:"100000"`

	// Investigation claim TTL — auto-expires if agent crashes.
	// IMPORTANT: Must be >= HITL timeout + execution buffer when agents use
	// cross-agent delegation with HITL gates. See impl plan §0.2.4.
	InvestigationTTL time.Duration `json:"investigation_ttl" env:"TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL" default:"30m"`

	// Max tokens of memory context injected into the LLM planning prompt.
	// Caps the enrichment to prevent prompt bloat.
	MaxEnrichmentTokens int `json:"max_enrichment_tokens" env:"TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS" default:"2000"`

	// Default retrieval weights for knowledge search scoring.
	DefaultRetrievalWeights RetrievalWeights `json:"default_retrieval_weights"`

	// Agent domain — groups agents for memory scoping.
	// Agents in the same domain see each other's ScopeSharedDomain events.
	// Set from TRUVAG3_AGENT_DOMAIN env var or explicit config.
	AgentDomain string `json:"agent_domain" env:"TRUVAG3_AGENT_DOMAIN" default:"default"`

	// RecentEventsLimit controls how many recent domain events the enrichment hook queries
	// for baseline situational awareness. Higher values show more cross-agent activity but
	// consume more prompt tokens. Default: 10.
	RecentEventsLimit int `json:"recent_events_limit" env:"TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT" default:"20"`

	// EnrichmentSummaryMaxTokens controls the maximum token budget for the compacted
	// domain activity summary injected into <agent_memory>. Higher values provide more
	// context but consume more of the planning LLM's context window.
	// Default: 500 tokens ≈ 2000 chars.
	EnrichmentSummaryMaxTokens int `json:"enrichment_summary_max_tokens" env:"TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS" default:"500"`

	// CompactionRecentDetail controls how many most recent raw events are appended after
	// the compacted digest for immediate detail access. Set to 0 to disable.
	// Default: 15.
	CompactionRecentDetail int `json:"compaction_recent_detail" env:"TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL" default:"15"`

	// CompactionRawLimit controls the maximum raw events fetched from the domain stream
	// before compaction. Higher values give the compactor more context but increase the
	// compaction LLM call's input size. Only used when ActivityCompactor is configured.
	// Default: 200.
	CompactionRawLimit int `json:"compaction_raw_limit" env:"TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT" default:"200"`

	// SummarizerModel overrides the AI model used for LLM-powered event summarization.
	// Supports model aliases ("fast", "smart") which resolve per provider, or concrete
	// model names (e.g., "gpt-4.1-mini", "claude-haiku-4-5-20251001").
	// Empty string uses the agent's default AI model.
	SummarizerModel string `json:"summarizer_model" env:"TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL" default:""`

	// DigestCacheTTL is the TTL for cached domain activity digests.
	// When expired, next request does full compaction. Default: 5m.
	DigestCacheTTL time.Duration `json:"digest_cache_ttl" env:"TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL" default:"5m"`

	// DigestIncrementalThreshold is the max new events for incremental digest update.
	// Above this, full recompaction is triggered. Default: 20.
	DigestIncrementalThreshold int `json:"digest_incremental_threshold" env:"TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD" default:"20"`

	// Compaction — disabled by default. Must be explicitly enabled.
	// When enabled, the application can call MemoryCompactor.RunCompaction().
	CompactionEnabled bool `json:"compaction_enabled" env:"TRUVAG3_SHARED_MEMORY_COMPACTION_ENABLED" default:"false"`
}

// ActivityCoordinationConfig contains settings for real-time agent coordination.
// Activity signals are transient (TTL-based) and separate from episodic memory.
type ActivityCoordinationConfig struct {
	// Enabled controls whether the activity coordination layer is active.
	// When false, no signals are emitted or read. Default: true.
	Enabled bool `json:"enabled" env:"TRUVAG3_ACTIVITY_COORDINATION_ENABLED" default:"true"`

	// SignalTTL is the time-to-live for activity signals. Signals expire after this duration.
	// Should be longer than the longest expected request duration. Default: 5m.
	SignalTTL time.Duration `json:"signal_ttl" env:"TRUVAG3_ACTIVITY_SIGNAL_TTL" default:"5m"`

	// MaxInPrompt is the maximum number of activity signals shown in <agent_coordination>.
	// Most recent first after filtering. Default: 10.
	MaxInPrompt int `json:"max_in_prompt" env:"TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT" default:"10"`

	// QueryMaxLen is the maximum characters of the request query included in signals.
	// Default: 200.
	QueryMaxLen int `json:"query_max_len" env:"TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN" default:"200"`
}

// ResilienceConfig contains fault tolerance and resilience patterns configuration.
// These patterns help protect the system from cascading failures and improve reliability.
type ResilienceConfig struct {
	CircuitBreaker CircuitBreakerConfig `json:"circuit_breaker"`
	Retry          RetryConfig          `json:"retry"`
	Timeout        TimeoutConfig        `json:"timeout"`
}

// CircuitBreakerConfig defines circuit breaker pattern settings.
// The circuit breaker prevents cascading failures by failing fast when a threshold
// of errors is reached. After a timeout period, it allows limited requests to test
// if the service has recovered.
type CircuitBreakerConfig struct {
	Enabled          bool          `json:"enabled" env:"TRUVAG3_CB_ENABLED" default:"false"`
	Threshold        int           `json:"threshold" env:"TRUVAG3_CB_THRESHOLD" default:"5"`
	Timeout          time.Duration `json:"timeout" env:"TRUVAG3_CB_TIMEOUT" default:"30s"`
	HalfOpenRequests int           `json:"half_open_requests" env:"TRUVAG3_CB_HALF_OPEN" default:"3"`
}

// RetryConfig defines retry pattern settings with exponential backoff.
// The retry interval increases exponentially up to MaxInterval.
// Formula: interval = min(InitialInterval * (Multiplier ^ attempt), MaxInterval)
type RetryConfig struct {
	MaxAttempts     int           `json:"max_attempts" env:"TRUVAG3_RETRY_MAX_ATTEMPTS" default:"3"`
	InitialInterval time.Duration `json:"initial_interval" env:"TRUVAG3_RETRY_INITIAL_INTERVAL" default:"1s"`
	MaxInterval     time.Duration `json:"max_interval" env:"TRUVAG3_RETRY_MAX_INTERVAL" default:"30s"`
	Multiplier      float64       `json:"multiplier" env:"TRUVAG3_RETRY_MULTIPLIER" default:"2.0"`
}

// TimeoutConfig defines timeout settings for various operations.
// These timeouts prevent operations from hanging indefinitely.
type TimeoutConfig struct {
	DefaultTimeout time.Duration `json:"default_timeout" env:"TRUVAG3_TIMEOUT_DEFAULT" default:"30s"`
	MaxTimeout     time.Duration `json:"max_timeout" env:"TRUVAG3_TIMEOUT_MAX" default:"5m"`
}

// LoggingConfig contains logging configuration.
// Supports structured (JSON) and human-readable (text) formats.
// In Kubernetes environments, JSON format is recommended for log aggregation.
type LoggingConfig struct {
	Level      string `json:"level" env:"TRUVAG3_LOG_LEVEL" default:"info"`
	Format     string `json:"format" env:"TRUVAG3_LOG_FORMAT" default:"json"`
	Output     string `json:"output" env:"TRUVAG3_LOG_OUTPUT" default:"stdout"`
	TimeFormat string `json:"time_format" env:"TRUVAG3_LOG_TIME_FORMAT" default:"2006-01-02T15:04:05.000Z07:00"`
}

// DevelopmentConfig contains settings for local development and testing.
// When Enabled=true, the framework uses development-friendly defaults:
// human-readable logs, mock services, and debug logging.
//
// WARNING: Never enable development mode in production!
type DevelopmentConfig struct {
	Enabled       bool `json:"enabled" env:"TRUVAG3_DEV_MODE" default:"false"`
	MockAI        bool `json:"mock_ai" env:"TRUVAG3_MOCK_AI" default:"false"`
	MockDiscovery bool `json:"mock_discovery" env:"TRUVAG3_MOCK_DISCOVERY" default:"false"`
	DebugLogging  bool `json:"debug_logging" env:"TRUVAG3_DEBUG" default:"false"`
	PrettyLogs    bool `json:"pretty_logs" env:"TRUVAG3_PRETTY_LOGS" default:"false"`
}

// KubernetesConfig contains Kubernetes-specific settings.
// The framework automatically detects Kubernetes environments by checking
// for the KUBERNETES_SERVICE_HOST environment variable.
// When running in Kubernetes, the framework adjusts defaults for
// containerized environments (e.g., binding to 0.0.0.0, JSON logging).
type KubernetesConfig struct {
	Enabled      bool   `json:"enabled" env:"KUBERNETES_SERVICE_HOST"`
	ServiceName  string `json:"service_name" env:"TRUVAG3_K8S_SERVICE_NAME"`
	ServicePort  int    `json:"service_port" env:"TRUVAG3_K8S_SERVICE_PORT" default:"80"`
	PodName      string `json:"pod_name" env:"HOSTNAME"`
	PodNamespace string `json:"pod_namespace" env:"TRUVAG3_K8S_NAMESPACE"`
	PodIP        string `json:"pod_ip" env:"TRUVAG3_K8S_POD_IP"`
	NodeName     string `json:"node_name" env:"TRUVAG3_K8S_NODE_NAME"`
	// PodAppLabel captures the pod's `metadata.labels.app` value, typically
	// injected via the downward API (fieldRef on metadata.labels['app']).
	// Populated only when TRUVAG3_K8S_POD_APP_LABEL is set. The framework uses
	// this purely to detect and warn on identity drift at startup — see
	// checkObservabilityIdentityAlignment. Absence of this env var is not an
	// error; the check simply skips.
	PodAppLabel            string `json:"pod_app_label" env:"TRUVAG3_K8S_POD_APP_LABEL"`
	ServiceAccountPath     string `json:"service_account_path" env:"TRUVAG3_K8S_SA_PATH" default:"/var/run/secrets/kubernetes.io/serviceaccount"`
	EnableServiceDiscovery bool   `json:"enable_service_discovery" env:"TRUVAG3_K8S_SERVICE_DISCOVERY" default:"true"`
	EnableLeaderElection   bool   `json:"enable_leader_election" env:"TRUVAG3_K8S_LEADER_ELECTION" default:"false"`
}

// Option is a functional option for configuring the framework.
// Options are applied in order and can return an error if the configuration is invalid.
//
// Example:
//
//	func WithCustomTimeout(timeout time.Duration) Option {
//	    return func(c *Config) error {
//	        if timeout <= 0 {
//	            return fmt.Errorf("timeout must be positive")
//	        }
//	        c.HTTP.ReadTimeout = timeout
//	        return nil
//	    }
//	}
type Option func(*Config) error

// DefaultConfig returns a configuration with sensible defaults.
// The defaults are adjusted based on the detected environment:
//   - Kubernetes: 0.0.0.0 binding, JSON logging, discovery enabled
//   - Local: localhost binding, text logging, development mode
//
// These defaults can be overridden using functional options or environment variables.
func DefaultConfig() *Config {
	cfg := &Config{
		Name:      "truvag3-agent",
		Port:      8080,
		Address:   "", // Will be set based on environment detection
		Namespace: "default",
		HTTP: HTTPConfig{
			ReadTimeout:       300 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      300 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20, // 1MB
			ShutdownTimeout:   10 * time.Second,
			EnableHealthCheck: true,
			HealthCheckPath:   "/health",
			EnableOpenAPI:     false, // Opt-in — see HTTPConfig.EnableOpenAPI godoc
			CORS: CORSConfig{
				Enabled:          false,
				AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
				AllowedHeaders:   []string{"Content-Type", "Authorization"},
				AllowCredentials: false,
				MaxAge:           86400,
			},
		},
		Discovery: DiscoveryConfig{
			Enabled:           false, // Disabled by default for local development
			Provider:          "redis",
			CacheEnabled:      true,
			CacheTTL:          5 * time.Minute,
			HeartbeatInterval: 0, // 0 means ttl/2 (default: 15s with 30s TTL)
			TTL:               30 * time.Second,
			RetryOnFailure:    false, // Disabled by default, opt-in
			RetryInterval:     30 * time.Second,
		},
		AI: AIConfig{
			Enabled:       false,
			Provider:      "openai",
			Model:         "gpt-4",
			Temperature:   0.7,
			MaxTokens:     2000,
			Timeout:       30 * time.Second,
			RetryAttempts: 3,
			RetryDelay:    1 * time.Second,
		},
		Telemetry: TelemetryConfig{
			Enabled:        false,
			Provider:       "otel",
			MetricsEnabled: true,
			TracingEnabled: true,
			Insecure:       true,
		},
		Memory: MemoryConfig{
			MaxSize:         1000,
			DefaultTTL:      1 * time.Hour,
			CleanupInterval: 10 * time.Minute,
		},
		SharedMemory: SharedMemoryConfig{
			Provider:            "noop",
			StreamMaxLen:        100000,
			InvestigationTTL:    30 * time.Minute,
			MaxEnrichmentTokens: 2000,
			DefaultRetrievalWeights: RetrievalWeights{
				Recency:    0.33,
				Relevance:  0.34,
				Importance: 0.33,
			},
			AgentDomain: "default",
		},
		ActivityCoordination: ActivityCoordinationConfig{
			Enabled:     true,
			SignalTTL:   5 * time.Minute,
			MaxInPrompt: 10,
			QueryMaxLen: 200,
		},
		Resilience: ResilienceConfig{
			CircuitBreaker: CircuitBreakerConfig{
				Enabled:          false,
				Threshold:        5,
				Timeout:          30 * time.Second,
				HalfOpenRequests: 3,
			},
			Retry: RetryConfig{
				MaxAttempts:     3,
				InitialInterval: 1 * time.Second,
				MaxInterval:     30 * time.Second,
				Multiplier:      2.0,
			},
			Timeout: TimeoutConfig{
				DefaultTimeout: 30 * time.Second,
				MaxTimeout:     5 * time.Minute,
			},
		},
		Logging: LoggingConfig{
			Level:      "info",
			Format:     "json",
			Output:     "stdout",
			TimeFormat: time.RFC3339Nano,
		},
		Development: DevelopmentConfig{
			Enabled:       false,
			MockAI:        false,
			MockDiscovery: false,
			DebugLogging:  false,
			PrettyLogs:    false,
		},
		Kubernetes: KubernetesConfig{
			ServicePort:            80,
			ServiceAccountPath:     "/var/run/secrets/kubernetes.io/serviceaccount",
			EnableServiceDiscovery: true,
			EnableLeaderElection:   false,
		},
	}

	// Detect environment and adjust defaults
	cfg.DetectEnvironment()

	return cfg
}

// DetectEnvironment automatically adjusts configuration based on the detected environment.
// This method is called automatically by DefaultConfig() and should not be called directly
// unless you're implementing custom environment detection logic.
//
// Detection criteria:
//   - Kubernetes: KUBERNETES_SERVICE_HOST environment variable is set
//   - Local: No Kubernetes environment variables detected
func (c *Config) DetectEnvironment() {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		// Kubernetes environment detected
		c.Kubernetes.Enabled = true
		c.Address = "0.0.0.0"      // Bind to all interfaces in K8s
		c.Discovery.Enabled = true // Enable discovery in K8s
		c.Discovery.RedisURL = "redis://redis.default.svc.cluster.local:6379"
		c.Logging.Format = "json" // Structured logs for K8s
	} else {
		// Local development environment
		c.Address = "localhost"
		c.Discovery.RedisURL = "redis://localhost:6379"

		// Enable development mode for local
		if os.Getenv("TRUVAG3_DEV_MODE") == "" {
			c.Development.Enabled = true
			c.Development.PrettyLogs = true
			c.Logging.Format = "text" // Human-readable logs
		}
	}
}

// LoadFromEnv loads configuration from environment variables and validates the result.
// Environment variables take precedence over defaults but are overridden by functional options.
//
// Variable naming convention:
//   - Framework-specific: TRUVAG3_<SETTING>
//   - Standard variables: REDIS_URL, OPENAI_API_KEY, OTEL_EXPORTER_OTLP_ENDPOINT
//
// Returns an error if environment variables contain invalid values or if validation fails.
func (c *Config) LoadFromEnv() error {
	if c.logger != nil {
		c.logger.Info("Loading configuration from environment", map[string]interface{}{
			"config_source": "environment_variables",
		})
	}

	envVarsLoaded := 0

	// Core settings
	if v := os.Getenv("TRUVAG3_AGENT_NAME"); v != "" {
		c.Name = v
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"operation": "load_from_env",
				"setting":   "agent_name",
				"source":    "TRUVAG3_AGENT_NAME",
				"set":       true,
			})
		}
	} else if v := os.Getenv(EnvServiceName); v != "" {
		// K8s service name is semantically equivalent to agent name in K8s deployments.
		// Uses EnvServiceName constant — single source of truth per design principles §3.3.
		c.Name = v
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"operation": "load_from_env",
				"setting":   "agent_name",
				"source":    EnvServiceName,
				"set":       true,
			})
		}
	}
	if v := os.Getenv("TRUVAG3_AGENT_ID"); v != "" {
		c.ID = v
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "agent_id",
				"source":  "TRUVAG3_AGENT_ID",
				"set":     true,
			})
		}
	}
	if v := os.Getenv("TRUVAG3_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			c.Port = port
			envVarsLoaded++
			if c.logger != nil {
				c.logger.Debug("Configuration loaded", map[string]interface{}{
					"setting": "port",
					"source":  "TRUVAG3_PORT",
					"set":     true,
				})
			}
		} else if c.logger != nil {
			c.logger.Warn("Invalid port in environment variable", map[string]interface{}{
				"TRUVAG3_PORT": v,
				"error":       err,
				"error_type":  fmt.Sprintf("%T", err),
			})
		}
	}
	if v := os.Getenv("TRUVAG3_ADDRESS"); v != "" {
		c.Address = v
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "address",
				"source":  "TRUVAG3_ADDRESS",
				"set":     true,
			})
		}
	}
	if v := os.Getenv("TRUVAG3_NAMESPACE"); v != "" {
		c.Namespace = v
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "namespace",
				"source":  "TRUVAG3_NAMESPACE",
				"set":     true,
			})
		}
	}

	// HTTP settings
	if v := os.Getenv("TRUVAG3_HTTP_READ_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.HTTP.ReadTimeout = d
		}
	}
	if v := os.Getenv("TRUVAG3_HTTP_WRITE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.HTTP.WriteTimeout = d
		}
	}

	// OpenAPI endpoint (disabled by default — see HTTPConfig.EnableOpenAPI)
	if v := os.Getenv("TRUVAG3_ENABLE_OPENAPI"); v != "" {
		c.HTTP.EnableOpenAPI = parseBool(v)
	}

	// CORS settings
	if v := os.Getenv("TRUVAG3_CORS_ENABLED"); v != "" {
		c.HTTP.CORS.Enabled = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_CORS_ORIGINS"); v != "" {
		c.HTTP.CORS.AllowedOrigins = parseStringList(v)
	}
	if v := os.Getenv("TRUVAG3_CORS_METHODS"); v != "" {
		c.HTTP.CORS.AllowedMethods = parseStringList(v)
	}
	if v := os.Getenv("TRUVAG3_CORS_HEADERS"); v != "" {
		c.HTTP.CORS.AllowedHeaders = parseStringList(v)
	}
	if v := os.Getenv("TRUVAG3_CORS_CREDENTIALS"); v != "" {
		c.HTTP.CORS.AllowCredentials = parseBool(v)
	}

	// Discovery settings
	if v := os.Getenv("TRUVAG3_DISCOVERY_ENABLED"); v != "" {
		c.Discovery.Enabled = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_DISCOVERY_PROVIDER"); v != "" {
		c.Discovery.Provider = v
	}
	if v := os.Getenv("TRUVAG3_REDIS_URL"); v != "" {
		c.Discovery.RedisURL = v
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "redis_url",
				"source":  "TRUVAG3_REDIS_URL",
				"set":     true,
			})
		}
	} else if v := os.Getenv("REDIS_URL"); v != "" {
		c.Discovery.RedisURL = v
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "redis_url",
				"source":  "REDIS_URL",
				"set":     true,
			})
		}
	}
	if v := os.Getenv("TRUVAG3_DISCOVERY_CACHE"); v != "" {
		c.Discovery.CacheEnabled = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_DISCOVERY_RETRY"); v != "" {
		c.Discovery.RetryOnFailure = parseBool(v)
		envVarsLoaded++
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "discovery_retry",
				"source":  "TRUVAG3_DISCOVERY_RETRY",
				"value":   c.Discovery.RetryOnFailure,
			})
		}
	}
	if v := os.Getenv("TRUVAG3_DISCOVERY_RETRY_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Discovery.RetryInterval = d
			envVarsLoaded++
			if c.logger != nil {
				c.logger.Debug("Configuration loaded", map[string]interface{}{
					"setting": "discovery_retry_interval",
					"source":  "TRUVAG3_DISCOVERY_RETRY_INTERVAL",
					"value":   d.String(),
				})
			}
		} else if c.logger != nil {
			c.logger.Warn("Invalid retry interval in environment variable", map[string]interface{}{
				"TRUVAG3_DISCOVERY_RETRY_INTERVAL": v,
				"error":                           err.Error(),
			})
		}
	}
	if v := os.Getenv("TRUVAG3_DISCOVERY_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Discovery.TTL = d
			envVarsLoaded++
			if c.logger != nil {
				c.logger.Debug("Configuration loaded", map[string]interface{}{
					"setting": "discovery_ttl",
					"source":  "TRUVAG3_DISCOVERY_TTL",
					"value":   d.String(),
				})
			}
		} else if c.logger != nil {
			c.logger.Warn("Invalid TTL in environment variable", map[string]interface{}{
				"TRUVAG3_DISCOVERY_TTL": v,
				"error":                err.Error(),
				"hint":                 "use Go duration format: 30s, 1m, etc.",
			})
		}
	}
	if v := os.Getenv("TRUVAG3_DISCOVERY_HEARTBEAT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Discovery.HeartbeatInterval = d
			envVarsLoaded++
			if c.logger != nil {
				c.logger.Debug("Configuration loaded", map[string]interface{}{
					"setting": "discovery_heartbeat",
					"source":  "TRUVAG3_DISCOVERY_HEARTBEAT",
					"value":   d.String(),
				})
			}
		} else if c.logger != nil {
			c.logger.Warn("Invalid heartbeat interval in environment variable", map[string]interface{}{
				"TRUVAG3_DISCOVERY_HEARTBEAT": v,
				"error":                      err.Error(),
				"hint":                       "use Go duration format: 10s, 500ms, etc.",
			})
		}
	}

	// AI settings
	if v := os.Getenv("TRUVAG3_AI_ENABLED"); v != "" {
		c.AI.Enabled = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_AI_API_KEY"); v != "" {
		c.AI.APIKey = v
		c.AI.Enabled = true // Auto-enable if API key is provided
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "ai_api_key",
				"source":  "TRUVAG3_AI_API_KEY",
				"set":     true,
			})
		}
	} else if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		c.AI.APIKey = v
		c.AI.Enabled = true // Auto-enable if OpenAI key is present
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "ai_api_key",
				"source":  "OPENAI_API_KEY",
				"set":     true,
			})
		}
	}
	if v := os.Getenv("TRUVAG3_AI_MODEL"); v != "" {
		c.AI.Model = v
	}
	if v := os.Getenv("TRUVAG3_AI_BASE_URL"); v != "" {
		c.AI.BaseURL = v
	}

	// Telemetry settings
	if v := os.Getenv("TRUVAG3_TELEMETRY_ENABLED"); v != "" {
		c.Telemetry.Enabled = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_TELEMETRY_ENDPOINT"); v != "" {
		c.Telemetry.Endpoint = v
		c.Telemetry.Enabled = true // Auto-enable if endpoint is provided
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "telemetry_endpoint",
				"source":  "TRUVAG3_TELEMETRY_ENDPOINT",
				"set":     true,
			})
		}
	} else if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		c.Telemetry.Endpoint = v
		c.Telemetry.Enabled = true // Auto-enable if OTEL endpoint is present
		if c.logger != nil {
			c.logger.Debug("Configuration loaded", map[string]interface{}{
				"setting": "telemetry_endpoint",
				"source":  "OTEL_EXPORTER_OTLP_ENDPOINT",
				"set":     true,
			})
		}
	}
	if v := os.Getenv("TRUVAG3_TELEMETRY_SERVICE_NAME"); v != "" {
		c.Telemetry.ServiceName = v
	} else if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		c.Telemetry.ServiceName = v
	} else if c.Telemetry.ServiceName == "" {
		c.Telemetry.ServiceName = c.Name // Default to agent name
	}

	// Memory settings
	// TRUVAG3_MEMORY_CLEANUP_INTERVAL — duration string (e.g. "5m", "10m", "1h").
	// Overrides Config.Memory.CleanupInterval. Used by MemoryStoreSweeper to bound
	// memory in agents using the framework-defaulted *MemoryStore.
	if v := os.Getenv("TRUVAG3_MEMORY_CLEANUP_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.Memory.CleanupInterval = d
		}
	}

	// Shared memory settings
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_PROVIDER"); v != "" {
		c.SharedMemory.Provider = v
	}
	if v := os.Getenv("TRUVAG3_AGENT_DOMAIN"); v != "" {
		c.SharedMemory.AgentDomain = v
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_STREAM_MAXLEN"); v != "" {
		if val, err := strconv.ParseInt(v, 10, 64); err == nil && val > 0 {
			c.SharedMemory.StreamMaxLen = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_INVESTIGATION_TTL"); v != "" {
		if val, err := time.ParseDuration(v); err == nil && val > 0 {
			c.SharedMemory.InvestigationTTL = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_ENRICHMENT_MAX_TOKENS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			c.SharedMemory.MaxEnrichmentTokens = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_RECENT_EVENTS_LIMIT"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			c.SharedMemory.RecentEventsLimit = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL"); v != "" {
		c.SharedMemory.SummarizerModel = v
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			c.SharedMemory.EnrichmentSummaryMaxTokens = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			c.SharedMemory.CompactionRawLimit = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_COMPACTION_RECENT_DETAIL"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val >= 0 {
			c.SharedMemory.CompactionRecentDetail = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL"); v != "" {
		if val, err := time.ParseDuration(v); err == nil && val > 0 {
			c.SharedMemory.DigestCacheTTL = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			c.SharedMemory.DigestIncrementalThreshold = val
		}
	}
	if v := os.Getenv("TRUVAG3_SHARED_MEMORY_COMPACTION_ENABLED"); v != "" {
		c.SharedMemory.CompactionEnabled = parseBool(v)
	}
	// Activity coordination env vars
	if v := os.Getenv("TRUVAG3_ACTIVITY_COORDINATION_ENABLED"); v != "" {
		c.ActivityCoordination.Enabled = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_ACTIVITY_SIGNAL_TTL"); v != "" {
		if val, err := time.ParseDuration(v); err == nil && val > 0 {
			c.ActivityCoordination.SignalTTL = val
		}
	}
	if v := os.Getenv("TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			c.ActivityCoordination.MaxInPrompt = val
		}
	}
	if v := os.Getenv("TRUVAG3_ACTIVITY_SIGNAL_QUERY_MAX_LEN"); v != "" {
		if val, err := strconv.Atoi(v); err == nil && val > 0 {
			c.ActivityCoordination.QueryMaxLen = val
		}
	}

	// Reuse existing Redis URL for shared memory if not separately configured
	if c.SharedMemory.RedisURL == "" {
		if v := os.Getenv("REDIS_URL"); v != "" {
			c.SharedMemory.RedisURL = v
		} else if v := os.Getenv("TRUVAG3_REDIS_URL"); v != "" {
			c.SharedMemory.RedisURL = v
		} else if c.Discovery.RedisURL != "" {
			c.SharedMemory.RedisURL = c.Discovery.RedisURL
		}
	}

	// Logging settings
	if v := os.Getenv("TRUVAG3_LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("TRUVAG3_LOG_FORMAT"); v != "" {
		c.Logging.Format = v
	}

	// Development settings
	if v := os.Getenv("TRUVAG3_DEV_MODE"); v != "" {
		c.Development.Enabled = parseBool(v)
		if c.Development.Enabled {
			c.Development.PrettyLogs = true
			c.Logging.Level = "debug"
			c.Logging.Format = "text"
		}
	}
	if v := os.Getenv("TRUVAG3_MOCK_AI"); v != "" {
		c.Development.MockAI = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_MOCK_DISCOVERY"); v != "" {
		c.Development.MockDiscovery = parseBool(v)
	}
	if v := os.Getenv("TRUVAG3_DEBUG"); v != "" {
		c.Development.DebugLogging = parseBool(v)
		if c.Development.DebugLogging {
			c.Logging.Level = "debug"
		}
	}

	// Kubernetes settings (auto-detect)
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		c.Kubernetes.Enabled = true
		if v := os.Getenv("HOSTNAME"); v != "" {
			c.Kubernetes.PodName = v
		}
		if v := os.Getenv("TRUVAG3_K8S_NAMESPACE"); v != "" {
			c.Kubernetes.PodNamespace = v
		}
		// Try to read namespace from service account
		if c.Kubernetes.PodNamespace == "" {
			if data, err := os.ReadFile(c.Kubernetes.ServiceAccountPath + "/namespace"); err == nil {
				c.Kubernetes.PodNamespace = strings.TrimSpace(string(data))
			}
		}
		if v := os.Getenv("TRUVAG3_K8S_SERVICE_NAME"); v != "" {
			c.Kubernetes.ServiceName = v
		}
		if v := os.Getenv("TRUVAG3_K8S_SERVICE_PORT"); v != "" {
			if port, err := strconv.Atoi(v); err == nil && port > 0 && port <= 65535 {
				c.Kubernetes.ServicePort = port
			}
		}
		if v := os.Getenv("TRUVAG3_K8S_POD_IP"); v != "" {
			c.Kubernetes.PodIP = v
		}
		if v := os.Getenv("TRUVAG3_K8S_NODE_NAME"); v != "" {
			c.Kubernetes.NodeName = v
		}
		if v := os.Getenv("TRUVAG3_K8S_POD_APP_LABEL"); v != "" {
			c.Kubernetes.PodAppLabel = v
		}
	}

	if err := c.Validate(); err != nil {
		if c.logger != nil {
			c.logger.Error("Configuration validation failed", map[string]interface{}{
				"error":         err.Error(),
				"error_type":    fmt.Sprintf("%T", err),
				"config_source": "environment_variables",
			})
		}
		return err
	}

	// Note: LOGGING_SOLUTION_ANALYSIS.md:1434 references "EnableDevelopmentMode" but actual field is "Enabled"
	if c.logger != nil {
		c.logger.Info("Configuration loading completed", map[string]interface{}{
			"discovery_enabled": c.Discovery.Enabled,
			"logging_level":     c.Logging.Level,
			"namespace":         c.Namespace,
			"development_mode":  c.Development.Enabled,
			"env_vars_loaded":   envVarsLoaded,
		})
	}

	return nil
}

// LoadFromFile loads configuration from a JSON file.
// The file should contain a JSON object matching the Config struct.
// File settings override environment variables but are overridden by functional options.
//
// Example JSON:
//
//	{
//	    "name": "my-agent",
//	    "port": 8080,
//	    "http": {
//	        "cors": {
//	            "enabled": true,
//	            "allowed_origins": ["https://example.com"]
//	        }
//	    }
//	}
func (c *Config) LoadFromFile(path string) error {
	if c.logger != nil {
		c.logger.Info("Loading configuration from file", map[string]interface{}{
			"file_path": path,
		})
	}

	// Clean the path to prevent directory traversal attacks
	cleanPath := filepath.Clean(path)

	// Verify the file has a safe extension
	ext := filepath.Ext(cleanPath)
	if ext != ".json" && ext != ".yaml" && ext != ".yml" {
		if c.logger != nil {
			c.logger.Error("Unsupported config file extension", map[string]interface{}{
				"file_path":         path,
				"clean_path":        cleanPath,
				"extension":         ext,
				"supported_formats": []string{".json", ".yaml", ".yml"},
			})
		}
		return fmt.Errorf("unsupported config file extension %s: %w", ext, ErrInvalidConfiguration)
	}

	// Check if the path is absolute and within expected directories
	if !filepath.IsAbs(cleanPath) {
		// If relative, resolve it relative to current directory
		wd, err := os.Getwd()
		if err != nil {
			if c.logger != nil {
				c.logger.Error("Failed to get working directory for relative config path", map[string]interface{}{
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"clean_path": cleanPath,
				})
			}
			return fmt.Errorf("failed to get working directory: %w", err)
		}
		cleanPath = filepath.Join(wd, cleanPath)

		if c.logger != nil {
			c.logger.Debug("Resolved relative config path", map[string]interface{}{
				"original_path": path,
				"resolved_path": cleanPath,
				"working_dir":   wd,
			})
		}
	}

	if c.logger != nil {
		c.logger.Debug("Reading configuration file", map[string]interface{}{
			"file_path": cleanPath,
			"extension": ext,
		})
	}

	// Read the file with the cleaned path
	data, err := os.ReadFile(filepath.Clean(cleanPath)) // nosec G304 -- path is validated
	if err != nil {
		if c.logger != nil {
			c.logger.Error("Failed to read config file", map[string]interface{}{
				"error":      err,
				"error_type": fmt.Sprintf("%T", err),
				"file_path":  cleanPath,
			})
		}
		return fmt.Errorf("failed to read config file %s: %w", cleanPath, err)
	}

	if c.logger != nil {
		c.logger.Debug("Config file read successfully", map[string]interface{}{
			"file_path": cleanPath,
			"file_size": len(data),
		})
	}

	// Parse based on extension
	switch ext {
	case ".json":
		if c.logger != nil {
			c.logger.Debug("Parsing JSON configuration file", map[string]interface{}{
				"file_path": cleanPath,
			})
		}

		if err := json.Unmarshal(data, c); err != nil {
			if c.logger != nil {
				c.logger.Error("Failed to parse JSON config file", map[string]interface{}{
					"error":      err,
					"error_type": fmt.Sprintf("%T", err),
					"file_path":  cleanPath,
					"file_size":  len(data),
				})
			}
			return fmt.Errorf("failed to parse JSON config file: %w", ErrInvalidConfiguration)
		}

		if c.logger != nil {
			c.logger.Info("Configuration file loaded successfully", map[string]interface{}{
				"file_path": cleanPath,
				"format":    "JSON",
				"file_size": len(data),
			})
		}

	case ".yaml", ".yml":
		if c.logger != nil {
			c.logger.Error("YAML configuration files not supported", map[string]interface{}{
				"file_path":         cleanPath,
				"extension":         ext,
				"supported_formats": []string{".json"},
			})
		}
		// For YAML support, we'd need to import gopkg.in/yaml.v3
		// For now, return an error for YAML files
		return fmt.Errorf("YAML config files not yet supported: %w", ErrInvalidConfiguration)
	}

	return nil
}

// Validate checks if the configuration is valid and returns an error if not.
// This method is called automatically by NewConfig() but can also be called
// manually after modifying configuration.
//
// Validation rules:
//   - Port must be between 1 and 65535
//   - Agent name is required
//   - AI API key is required when AI is enabled (unless using mock)
//   - Telemetry endpoint is required when telemetry is enabled
//   - Redis URL is required when Redis discovery is enabled (unless using mock)
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		// Preserve exact message for test compatibility
		return &FrameworkError{
			Op:      "Config.Validate",
			Kind:    "config",
			Message: fmt.Sprintf("invalid port: %d", c.Port),
			Err:     ErrInvalidConfiguration,
		}
	}

	if c.Name == "" {
		// Preserve exact message for test compatibility
		return &FrameworkError{
			Op:      "Config.Validate",
			Kind:    "config",
			Message: "agent name is required",
			Err:     ErrMissingConfiguration,
		}
	}

	if c.AI.Enabled && c.AI.APIKey == "" && !c.Development.MockAI {
		// Preserve exact message for test compatibility
		return &FrameworkError{
			Op:      "Config.Validate",
			Kind:    "config",
			Message: "AI API key is required when AI is enabled (or use mock AI in development)",
			Err:     ErrMissingConfiguration,
		}
	}

	if c.Telemetry.Enabled && c.Telemetry.Endpoint == "" {
		// Preserve exact message for test compatibility
		return &FrameworkError{
			Op:      "Config.Validate",
			Kind:    "config",
			Message: "telemetry endpoint is required when telemetry is enabled",
			Err:     ErrMissingConfiguration,
		}
	}

	if c.Discovery.Enabled && c.Discovery.Provider == "redis" && c.Discovery.RedisURL == "" && !c.Development.MockDiscovery {
		// Preserve exact message for test compatibility
		return &FrameworkError{
			Op:      "Config.Validate",
			Kind:    "config",
			Message: "redis URL is required for Redis discovery provider (or use mock discovery in development)",
			Err:     ErrMissingConfiguration,
		}
	}

	return nil
}

// Helper functions

// parseStringList splits a comma-separated string into a slice of strings.
// Whitespace is trimmed from each element, and empty strings are filtered out.
// Example: "a, b, c" -> ["a", "b", "c"]
func parseStringList(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// parseBool converts a string to a boolean value.
// Accepts: "true", "1", "yes", "on" (case-insensitive) as true.
// Everything else is false.
func parseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "1" || s == "yes" || s == "on"
}

// Functional Options

// WithName sets the agent name.
// The name is used for identification in service discovery and logging.
// If not set, defaults to "truvag3-agent".
func WithName(name string) Option {
	return func(c *Config) error {
		c.Name = name
		return nil
	}
}

// WithPort sets the HTTP server port.
// Must be between 1 and 65535.
// Returns an error if the port is invalid.
func WithPort(port int) Option {
	return func(c *Config) error {
		if port < 1 || port > 65535 {
			// Preserve exact message for test compatibility
			return &FrameworkError{
				Op:      "WithPort",
				Kind:    "config",
				Message: fmt.Sprintf("invalid port: %d", port),
				Err:     ErrInvalidConfiguration,
			}
		}
		c.Port = port
		return nil
	}
}

// WithAddress sets the bind address for the HTTP server.
// Common values:
//   - "localhost" or "127.0.0.1" for local only
//   - "0.0.0.0" for all interfaces (required in containers)
//   - Specific IP for multi-homed hosts
func WithAddress(address string) Option {
	return func(c *Config) error {
		c.Address = address
		return nil
	}
}

// WithNamespace sets the logical namespace for the agent.
// Used for multi-tenancy and environment separation (e.g., "production", "staging").
// This is a logical grouping, not a Kubernetes namespace.
func WithNamespace(namespace string) Option {
	return func(c *Config) error {
		c.Namespace = namespace
		return nil
	}
}

// WithCORS enables CORS with specific allowed origins.
// Supports wildcard patterns:
//   - "*" allows all origins (not recommended for production)
//   - "*.example.com" allows all subdomains
//   - "http://localhost:*" allows any localhost port
//
// The credentials parameter controls whether cookies and auth headers are allowed.
// Be cautious when enabling credentials with wildcard origins.
func WithCORS(origins []string, credentials bool) Option {
	return func(c *Config) error {
		c.HTTP.CORS.Enabled = true
		c.HTTP.CORS.AllowedOrigins = origins
		c.HTTP.CORS.AllowCredentials = credentials
		return nil
	}
}

// WithOpenAPI controls whether the component exposes GET /openapi.json.
//
// The endpoint is disabled by default because it publishes the full API
// surface of the component (every capability, input/output schema, and
// endpoint path) to anyone who can reach the pod. Enabling it in production
// would leak internal contract information.
//
// Enable it for development and staging deployments where Swagger UI or a
// similar API browser is in use. For production, leave it off.
//
// This option takes precedence over the TRUVAG3_ENABLE_OPENAPI environment
// variable. Use the env var when you want to flip the default for an entire
// deployment without changing code.
//
// Example:
//
//	// Dev: enable explicitly
//	framework, _ := core.NewFramework(tool, core.WithOpenAPI(true))
//
//	// Or in the deployment environment:
//	// TRUVAG3_ENABLE_OPENAPI=true
func WithOpenAPI(enabled bool) Option {
	return func(c *Config) error {
		c.HTTP.EnableOpenAPI = enabled
		return nil
	}
}

// WithCORSDefaults enables CORS with permissive defaults.
// Allows all origins, methods, and headers with credentials.
//
// WARNING: This is intended for development only!
// Never use this in production as it bypasses CORS security.
func WithCORSDefaults() Option {
	return func(c *Config) error {
		c.HTTP.CORS.Enabled = true
		c.HTTP.CORS.AllowedOrigins = []string{"*"}
		c.HTTP.CORS.AllowedMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"}
		c.HTTP.CORS.AllowedHeaders = []string{"*"}
		c.HTTP.CORS.AllowCredentials = true
		return nil
	}
}

// WithMiddleware adds custom HTTP middleware to the handler chain.
// Middleware functions wrap the HTTP handler, with earlier middleware being outermost.
//
// This enables application-level injection of telemetry middleware (e.g., tracing)
// without the core module importing telemetry - following framework design principles.
//
// The middleware is applied AFTER the built-in middleware (CORS, Logging, Recovery),
// making custom middleware the outermost layer in the chain.
//
// Example:
//
//	// In your tool's main.go, add tracing middleware
//	framework, _ := core.NewFramework(tool,
//	    core.WithName("weather-tool"),
//	    core.WithMiddleware(
//	        telemetry.TracingMiddleware("weather-tool"),
//	    ),
//	)
//
// Multiple middleware can be added in a single call - they are applied in order:
//
//	core.WithMiddleware(
//	    telemetry.TracingMiddleware("my-service"),  // Outermost
//	    myCustomAuthMiddleware,                      // Inner
//	)
func WithMiddleware(middleware ...func(http.Handler) http.Handler) Option {
	return func(c *Config) error {
		c.HTTP.Middleware = append(c.HTTP.Middleware, middleware...)
		return nil
	}
}

// WithRedisURL sets the Redis connection URL for service discovery.
// Format: redis://[user:password@]host:port/db
// Examples:
//   - redis://localhost:6379
//   - redis://user:pass@redis.example.com:6379/0
//   - redis://redis.default.svc.cluster.local:6379
//
// This automatically enables discovery when set. Memory storage is in-process
// only; this URL does not affect it.
func WithRedisURL(url string) Option {
	return func(c *Config) error {
		c.Discovery.RedisURL = url
		c.Discovery.Enabled = true // Auto-enable discovery when Redis is configured
		return nil
	}
}

// WithDiscovery enables or disables service discovery with the specified provider.
// Currently supported providers:
//   - "redis": Redis-based discovery (auto-configures RedisURL from environment or defaults to localhost)
//   - "mock": In-memory mock for testing
//
// When disabled, the agent runs in standalone mode without discovery.
func WithDiscovery(enabled bool, provider string) Option {
	return func(c *Config) error {
		c.Discovery.Enabled = enabled
		c.Discovery.Provider = provider

		// Auto-configure Redis URL for Redis provider
		if enabled && provider == "redis" {
			// Check if RedisURL was explicitly set by user configuration
			// We can distinguish this from LoadFromEnv by checking if it's not one of the common env values
			currentURL := c.Discovery.RedisURL
			wasExplicitlySet := currentURL != "" &&
				currentURL != os.Getenv("REDIS_URL") &&
				currentURL != os.Getenv("TRUVAG3_REDIS_URL")

			if !wasExplicitlySet {
				// Apply proper precedence: REDIS_URL takes precedence over TRUVAG3_REDIS_URL
				redisURL := os.Getenv("REDIS_URL")
				if redisURL != "" {
					c.Discovery.RedisURL = redisURL
				} else if truvag3RedisURL := os.Getenv("TRUVAG3_REDIS_URL"); truvag3RedisURL != "" {
					c.Discovery.RedisURL = truvag3RedisURL
				} else if currentURL == "" {
					// Use sensible default for development only if no URL was set
					c.Discovery.RedisURL = "redis://localhost:6379"
				}
			}
		} else if !enabled || provider != "redis" {
			// Clear RedisURL if discovery is disabled or non-Redis provider
			c.Discovery.RedisURL = ""
		}
		return nil
	}
}

// WithRedisDiscovery is a convenience function that configures Redis-based discovery
// with the specified Redis URL. This is equivalent to calling:
//
//	WithDiscovery(true, "redis") + WithRedisURL(redisURL)
//
// but more explicit and convenient for Redis-specific setups.
func WithRedisDiscovery(redisURL string) Option {
	return func(c *Config) error {
		c.Discovery.Enabled = true
		c.Discovery.Provider = "redis"
		c.Discovery.RedisURL = redisURL
		return nil
	}
}

// WithDiscoveryCacheEnabled enables or disables discovery result caching.
// When enabled, discovery results are cached for CacheTTL duration to reduce
// load on the discovery backend. Recommended for production.
func WithDiscoveryCacheEnabled(enabled bool) Option {
	return func(c *Config) error {
		c.Discovery.CacheEnabled = enabled
		return nil
	}
}

// WithDiscoveryTTL sets the TTL for service registration in Redis.
// Minimum 5 seconds. If 0 or negative, defaults to 30 seconds.
// The actual clamping is applied in NewRedisRegistryWithOptions.
func WithDiscoveryTTL(ttl time.Duration) Option {
	return func(c *Config) error {
		c.Discovery.TTL = ttl
		return nil
	}
}

// WithHeartbeatInterval sets how often the service refreshes its registration.
// Minimum 2 seconds. Must be shorter than TTL. If 0, defaults to TTL/2.
// The actual clamping is applied in StartHeartbeat.
func WithHeartbeatInterval(interval time.Duration) Option {
	return func(c *Config) error {
		c.Discovery.HeartbeatInterval = interval
		return nil
	}
}

// WithOpenAIAPIKey sets the OpenAI API key and automatically enables AI features.
// The key should be a valid OpenAI API key starting with "sk-".
// This is a convenience method equivalent to:
//
//	WithAI(true, "openai", key)
//
// For security, prefer loading the key from environment variables or secrets.
func WithOpenAIAPIKey(key string) Option {
	return func(c *Config) error {
		c.AI.Enabled = true
		c.AI.Provider = "openai"
		c.AI.APIKey = key
		return nil
	}
}

// WithAI configures AI client settings.
// Parameters:
//   - enabled: Whether to initialize AI features
//   - provider: AI provider ("openai", "anthropic", "mock")
//   - apiKey: API key for the provider (not needed for "mock")
//
// When enabled=false, AI features are completely disabled regardless of other settings.
func WithAI(enabled bool, provider, apiKey string) Option {
	return func(c *Config) error {
		c.AI.Enabled = enabled
		c.AI.Provider = provider
		c.AI.APIKey = apiKey
		return nil
	}
}

// WithAIModel sets the AI model to use.
// Common values:
//   - OpenAI: "gpt-4", "gpt-4-turbo", "gpt-3.5-turbo"
//   - Anthropic: "claude-3-5-sonnet-20241022", "claude-3-opus-20240229"
//
// Check your provider's documentation for available models.
func WithAIModel(model string) Option {
	return func(c *Config) error {
		c.AI.Model = model
		return nil
	}
}

// WithTelemetry enables telemetry with the specified endpoint.
// The endpoint should be an OpenTelemetry Protocol (OTLP) receiver.
// Examples:
//   - "http://localhost:4317" (local Jaeger)
//   - "http://otel-collector:4317" (Kubernetes)
//   - "https://otel.example.com:443" (cloud provider)
//
// When enabled, both metrics and tracing are collected by default.
func WithTelemetry(enabled bool, endpoint string) Option {
	return func(c *Config) error {
		c.Telemetry.Enabled = enabled
		c.Telemetry.Endpoint = endpoint
		if c.Telemetry.ServiceName == "" {
			c.Telemetry.ServiceName = c.Name
		}
		return nil
	}
}

// WithEnableMetrics enables or disables metrics collection.
// Metrics include request counts, latencies, error rates, etc.
// Requires telemetry to be enabled with an endpoint.
// Metrics are exported via OpenTelemetry protocol.
func WithEnableMetrics(enabled bool) Option {
	return func(c *Config) error {
		c.Telemetry.MetricsEnabled = enabled
		if enabled && c.Telemetry.Endpoint != "" {
			c.Telemetry.Enabled = true
		}
		return nil
	}
}

// WithEnableTracing enables or disables distributed tracing.
// Tracing provides detailed request flow across services.
// Requires telemetry to be enabled with an endpoint.
// Traces are exported via OpenTelemetry protocol.
func WithEnableTracing(enabled bool) Option {
	return func(c *Config) error {
		c.Telemetry.TracingEnabled = enabled
		if enabled && c.Telemetry.Endpoint != "" {
			c.Telemetry.Enabled = true
		}
		return nil
	}
}

// WithOTELEndpoint sets the OpenTelemetry endpoint and automatically enables telemetry.
// This is a convenience method equivalent to:
//
//	WithTelemetry(true, endpoint)
//
// The endpoint should be an OTLP receiver address.
func WithOTELEndpoint(endpoint string) Option {
	return func(c *Config) error {
		c.Telemetry.Enabled = true
		c.Telemetry.Provider = "otel"
		c.Telemetry.Endpoint = endpoint
		return nil
	}
}

// WithLogLevel sets the minimum logging level.
// Valid levels (from least to most verbose):
//   - "error": Only errors
//   - "warn": Warnings and above
//   - "info": Informational messages and above (default)
//   - "debug": Debug messages and above
//
// Debug level should not be used in production due to performance impact.
func WithLogLevel(level string) Option {
	return func(c *Config) error {
		c.Logging.Level = level
		return nil
	}
}

// WithLogFormat sets the logging output format.
// Valid formats:
//   - "json": Structured JSON for log aggregation (recommended for production)
//   - "text": Human-readable format (recommended for development)
//
// JSON format is automatically selected in Kubernetes environments.
func WithLogFormat(format string) Option {
	return func(c *Config) error {
		c.Logging.Format = format
		return nil
	}
}

// WithCircuitBreaker enables the circuit breaker pattern for fault tolerance.
// Parameters:
//   - threshold: Number of consecutive failures before opening the circuit
//   - timeout: Duration to wait before attempting to close the circuit
//
// The circuit breaker prevents cascading failures by failing fast when
// a service is unhealthy, giving it time to recover.
func WithCircuitBreaker(threshold int, timeout time.Duration) Option {
	return func(c *Config) error {
		c.Resilience.CircuitBreaker.Enabled = true
		c.Resilience.CircuitBreaker.Threshold = threshold
		c.Resilience.CircuitBreaker.Timeout = timeout
		return nil
	}
}

// WithRetry configures automatic retry with exponential backoff.
// Parameters:
//   - maxAttempts: Maximum number of retry attempts (including initial)
//   - initialInterval: Initial delay between retries
//
// The retry interval doubles after each failure up to MaxInterval.
// Use this for transient failures like network issues.
func WithRetry(maxAttempts int, initialInterval time.Duration) Option {
	return func(c *Config) error {
		c.Resilience.Retry.MaxAttempts = maxAttempts
		c.Resilience.Retry.InitialInterval = initialInterval
		return nil
	}
}

// WithKubernetes enables Kubernetes-specific features.
// Parameters:
//   - serviceDiscovery: Use Kubernetes service discovery instead of Redis
//   - leaderElection: Enable leader election for singleton patterns
//
// These features require proper RBAC permissions in the cluster.
// The framework automatically detects Kubernetes environments, so this
// is only needed to enable specific features.
func WithKubernetes(serviceDiscovery, leaderElection bool) Option {
	return func(c *Config) error {
		c.Kubernetes.EnableServiceDiscovery = serviceDiscovery
		c.Kubernetes.EnableLeaderElection = leaderElection
		return nil
	}
}

// WithConfigFile loads configuration from a JSON file.
// The file path can be absolute or relative to the working directory.
// File configuration is applied before other options, so options
// can override file settings.
//
// This is useful for complex configurations or environment-specific settings.
func WithConfigFile(path string) Option {
	return func(c *Config) error {
		return c.LoadFromFile(path)
	}
}

// WithDevelopmentMode enables development mode with developer-friendly defaults.
// When enabled:
//   - Pretty (human-readable) logs
//   - Debug log level
//   - Text log format
//   - Relaxed validation
//
// WARNING: Never enable in production! This mode sacrifices
// performance and security for developer convenience.
func WithDevelopmentMode(enabled bool) Option {
	return func(c *Config) error {
		c.Development.Enabled = enabled
		if enabled {
			c.Development.PrettyLogs = true
			c.Logging.Format = "text"
			c.Logging.Level = "debug"
		}
		return nil
	}
}

// WithMockAI enables mock AI responses for testing without API calls.
// When enabled, the AI client returns predetermined responses instead
// of making actual API calls. Useful for:
//   - Unit testing
//   - Development without API keys
//   - Cost savings during development
//
// Mock responses are deterministic but not intelligent.
func WithMockAI(enabled bool) Option {
	return func(c *Config) error {
		c.Development.MockAI = enabled
		if enabled {
			c.AI.Enabled = true // Enable AI with mock provider
		}
		return nil
	}
}

// WithMockDiscovery enables in-memory mock discovery for testing.
// When enabled, service discovery uses local memory instead of Redis.
// Useful for:
//   - Unit testing
//   - Local development without Redis
//   - Isolated testing environments
//
// Note: Mock discovery is not distributed across instances.
func WithMockDiscovery(enabled bool) Option {
	return func(c *Config) error {
		c.Development.MockDiscovery = enabled
		if enabled {
			c.Discovery.Enabled = true // Enable discovery with mock provider
		}
		return nil
	}
}

// WithLogger sets a logger for configuration operations.
// This logger will be used for logging during config loading, parsing, and validation.
// If not set, configuration operations will be performed silently.
//
// Example:
//
//	cfg, err := NewConfig(
//	    WithLogger(myLogger),
//	    WithName("my-agent"),
//	)
func WithLogger(logger Logger) Option {
	return func(c *Config) error {
		c.logger = logger
		return nil
	}
}

// NewConfig creates a new configuration with the provided options.
// Configuration is applied in the following order:
//  1. Default values from DefaultConfig()
//  2. Environment variables via LoadFromEnv()
//  3. Functional options (highest priority)
//  4. Validation via Validate()
//
// Returns an error if any option fails or if the final configuration is invalid.
//
// Example:
//
//	cfg, err := NewConfig(
//	    WithName("my-agent"),
//	    WithPort(8080),
//	    WithRedisURL("redis://localhost:6379"),
//	)
//	if err != nil {
//	    return err
//	}
func NewConfig(opts ...Option) (*Config, error) {
	// Start with defaults
	cfg := DefaultConfig()

	// Load from environment first (includes validation per spec)
	if err := cfg.LoadFromEnv(); err != nil {
		return nil, fmt.Errorf("failed to load env config: %w", err)
	}

	// Apply functional options (these override env vars)
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	if cfg.logger == nil {
		logger := NewProductionLogger(cfg.Logging, cfg.Development, cfg.Name)

		// Track for metrics enabling when telemetry available
		if prodLogger, ok := logger.(*ProductionLogger); ok {
			trackLogger(prodLogger)
		}

		cfg.logger = logger
	}

	// Advisory startup check for observability identity drift. Never fails
	// startup; logs WARN if the pod's app: label disagrees with cfg.Name or
	// cfg.Telemetry.ServiceName. No-op when TRUVAG3_K8S_POD_APP_LABEL is unset
	// (non-K8s deployments and K8s pods that don't inject the pod label via
	// the downward API).
	cfg.checkObservabilityIdentityAlignment()

	// Validate final configuration after options applied
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// observabilityIdentityDocRef is the canonical doc pointer cited in the
// remediation field of the identity-drift warning. Centralized here so the
// reference can be updated in one place if the doc is ever restructured.
const observabilityIdentityDocRef = "docs/TOOL_DEVELOPMENT_GUIDE.md §8 Observability Identity"

// checkObservabilityIdentityAlignment verifies that the pod's app: label agrees
// with the framework's cfg.Name (which drives the log-body "service" field and
// service registration) and cfg.Telemetry.ServiceName (which drives the SDK
// resource attribute consumed by Jaeger and Prometheus).
//
// When these disagree, the cluster-wide logs pipeline (which derives
// service_name from the pod label via k8sattributes) will label records under
// one name while traces and metrics land under another, producing split-brain
// observability where the same workload has different identities in different
// backends.
//
// This check is strictly advisory: it never fails startup, and it becomes a
// no-op when cfg.Kubernetes.PodAppLabel is empty — which covers non-Kubernetes
// deployments and Kubernetes pods that have not been updated to inject the pod
// label via the downward API. Opt in by adding the following env entry to your
// pod manifest:
//
//	env:
//	- name: TRUVAG3_K8S_POD_APP_LABEL
//	  valueFrom:
//	    fieldRef:
//	      fieldPath: metadata.labels['app']
//
// See observabilityIdentityDocRef (above) for the full alignment rule.
func (c *Config) checkObservabilityIdentityAlignment() {
	if c.logger == nil || c.Kubernetes.PodAppLabel == "" {
		return
	}
	podLabel := c.Kubernetes.PodAppLabel

	nameDrift := c.Name != "" && c.Name != podLabel
	telemetryDrift := c.Telemetry.ServiceName != "" && c.Telemetry.ServiceName != podLabel
	if !nameDrift && !telemetryDrift {
		return
	}

	details := []string{}
	if nameDrift {
		details = append(details,
			fmt.Sprintf("framework name %q (set via core.WithName or TRUVAG3_AGENT_NAME) differs from pod app label %q — log body \"service\" field and Loki service_name will disagree",
				c.Name, podLabel))
	}
	if telemetryDrift {
		details = append(details,
			fmt.Sprintf("telemetry service name %q (set via OTEL_SERVICE_NAME or TRUVAG3_TELEMETRY_SERVICE_NAME) differs from pod app label %q — Jaeger traces and Prometheus metrics will surface under a different identity than Loki logs",
				c.Telemetry.ServiceName, podLabel))
	}

	c.logger.Warn("Observability identity drift detected at startup", map[string]interface{}{
		"operation":              "observability_identity_check",
		"pod_app_label":          podLabel,
		"framework_name":         c.Name,
		"telemetry_service_name": c.Telemetry.ServiceName,
		"drift_details":          details,
		"remediation":            "Pick one canonical name and use it identically in: pod `app:` label, core.WithName(...), and OTEL_SERVICE_NAME (or leave OTEL_SERVICE_NAME unset — it defaults to cfg.Name). See " + observabilityIdentityDocRef + ".",
	})
}

// ============================================================================
// ProductionLogger Implementation - Layered Observability Architecture
// ============================================================================

// LogLevel represents logging severity levels with proper hierarchy
type LogLevel int

const (
	// LogLevelDebug is the most verbose level - for development/troubleshooting
	LogLevelDebug LogLevel = iota
	// LogLevelInfo is the default production level - standard operations
	LogLevelInfo
	// LogLevelWarn is for warnings and potential issues
	LogLevelWarn
	// LogLevelError is the highest severity - only critical errors
	LogLevelError
)

// parseLogLevel converts string level to LogLevel enum
// Returns LogLevelInfo as safe default for invalid inputs
func parseLogLevel(level string) LogLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return LogLevelDebug
	case "warn", "warning":
		return LogLevelWarn
	case "error":
		return LogLevelError
	case "info", "":
		return LogLevelInfo
	default:
		// Unknown level defaults to info (safe fallback)
		return LogLevelInfo
	}
}

// ProductionLogger provides layered observability for framework operations
type ProductionLogger struct {
	level          LogLevel // Numeric level for efficient comparison
	serviceName    string
	component      string // Component identifier (e.g., "framework/core", "agent/<name>", "tool/<name>")
	format         string
	output         io.Writer
	metricsEnabled bool // Metrics layer (enabled when telemetry available)
}

// NewProductionLogger creates a logger from LoggingConfig
func NewProductionLogger(logging LoggingConfig, dev DevelopmentConfig, serviceName string) Logger {
	var output io.Writer = os.Stdout
	if logging.Output == "stderr" {
		output = os.Stderr
	}

	// Parse log level - development mode overrides to debug
	level := parseLogLevel(logging.Level)
	if dev.Enabled || dev.DebugLogging {
		level = LogLevelDebug
	}

	return &ProductionLogger{
		level:          level,
		serviceName:    serviceName,
		component:      "framework/core", // Default component for framework internals
		format:         logging.Format,
		output:         output,
		metricsEnabled: false, // Enabled by telemetry module when available
	}
}

// EnableMetrics is called by telemetry module to enable metrics layer
func (p *ProductionLogger) EnableMetrics() {
	p.metricsEnabled = true
}

// WithComponent creates a child logger with specific component context.
// This allows different parts of the application to have their own
// component identifier while sharing the same base configuration.
//
// Component naming convention:
//   - "framework/core"          - Core framework (discovery, registry, config)
//   - "framework/orchestration" - Orchestration module
//   - "framework/ai"            - AI module
//   - "framework/resilience"    - Resilience patterns
//   - "agent/<name>"            - User agents (e.g., "agent/travel-research-orchestration")
//   - "tool/<name>"             - User tools (e.g., "tool/weather-service")
func (p *ProductionLogger) WithComponent(component string) Logger {
	return &ProductionLogger{
		level:          p.level,
		serviceName:    p.serviceName,
		component:      component,
		format:         p.format,
		output:         p.output,
		metricsEnabled: p.metricsEnabled,
	}
}

// GetComponent returns the current component identifier for this logger.
// This is useful for testing and debugging to verify the correct component
// was set during logger creation.
func (p *ProductionLogger) GetComponent() string {
	return p.component
}

// Debug logs debug-level messages (only when level is Debug)
func (p *ProductionLogger) Debug(msg string, fields map[string]interface{}) {
	if p.level <= LogLevelDebug {
		p.logEvent("DEBUG", msg, fields, nil)
	}
}

// Info logs informational messages (when level is Info or Debug)
func (p *ProductionLogger) Info(msg string, fields map[string]interface{}) {
	if p.level <= LogLevelInfo {
		p.logEvent("INFO", msg, fields, nil)
	}
}

// InfoWithContext logs informational messages with context
func (p *ProductionLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	if p.level <= LogLevelInfo {
		p.logEvent("INFO", msg, fields, ctx)
	}
}

// Warn logs warning messages (when level is Warn, Info, or Debug)
func (p *ProductionLogger) Warn(msg string, fields map[string]interface{}) {
	if p.level <= LogLevelWarn {
		p.logEvent("WARN", msg, fields, nil)
	}
}

// Error logs error messages (always logged - highest severity)
func (p *ProductionLogger) Error(msg string, fields map[string]interface{}) {
	// Errors always log regardless of level (highest severity)
	p.logEvent("ERROR", msg, fields, nil)
}

// ErrorWithContext logs error messages with context
func (p *ProductionLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	// Errors always log regardless of level (highest severity)
	p.logEvent("ERROR", msg, fields, ctx)
}

// WarnWithContext logs warning messages with context for request correlation
func (p *ProductionLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	if p.level <= LogLevelWarn {
		p.logEvent("WARN", msg, fields, ctx)
	}
}

// DebugWithContext logs debug information with context for request correlation
func (p *ProductionLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	if p.level <= LogLevelDebug {
		p.logEvent("DEBUG", msg, fields, ctx)
	}
}

// Core logging implementation with all three layers
func (p *ProductionLogger) logEvent(level, msg string, fields map[string]interface{}, ctx context.Context) {
	timestamp := time.Now().Format(time.RFC3339)

	if p.format == "json" {
		// Structured logging for production log aggregation
		logEntry := map[string]interface{}{
			"timestamp": timestamp,
			"level":     level,
			"service":   p.serviceName,
			"component": p.component,
			"message":   msg,
		}

		// LAYER 3: Add trace context when available (OTel semantic conventions)
		// Fields like trace_id, span_id are added at root level per OpenTelemetry spec
		// See: https://opentelemetry.io/docs/specs/otel/compatibility/logging_trace_context/
		if ctx != nil && p.metricsEnabled {
			if baggage := getContextBaggage(ctx); len(baggage) > 0 {
				for k, v := range baggage {
					logEntry[k] = v
				}
			}
		}

		// Add all fields
		for k, v := range fields {
			logEntry[k] = v
		}

		if data, err := json.Marshal(logEntry); err == nil {
			_, _ = fmt.Fprintln(p.output, string(data)) // Error writing logs can be safely ignored
		}
	} else {
		// Human-readable for local development
		traceInfo := ""
		if ctx != nil && p.metricsEnabled {
			if baggage := getContextBaggage(ctx); baggage["request_id"] != "" {
				traceInfo = fmt.Sprintf("[req=%s] ", baggage["request_id"])
			}
		}

		var fieldStr strings.Builder
		if len(fields) > 0 {
			fieldStr.WriteString(" ")
			for k, v := range fields {
				fmt.Fprintf(&fieldStr, "%s=%v ", k, v)
			}
		}

		_, _ = fmt.Fprintf(p.output, "%s [%s] [%s] %s%s%s\n",
			timestamp, level, p.serviceName, traceInfo, msg, fieldStr.String()) // Error writing logs can be safely ignored
	}

	if p.metricsEnabled {
		p.emitFrameworkMetric(level, msg, fields, ctx)
	}
}

// Metrics emission with cardinality protection
func (p *ProductionLogger) emitFrameworkMetric(level, msg string, fields map[string]interface{}, ctx context.Context) {
	// Build labels with cardinality awareness
	labels := []string{
		"level", level,
		"service", p.serviceName,
		"component", "framework",
	}

	// Add only low-cardinality fields as labels
	for k, v := range fields {
		switch k {
		case "operation", "status", "error_type", "service_type", "provider":
			labels = append(labels, k, fmt.Sprintf("%v", v))
		}
	}

	// Emit with context when available (enables correlation)
	if ctx != nil {
		emitMetricWithContext(ctx, "truvag3.framework.operations", 1.0, labels...)
	} else {
		emitMetric("truvag3.framework.operations", 1.0, labels...)
	}
}

// Helper functions for weak coupling to telemetry
func emitMetric(name string, value float64, labels ...string) {
	if globalMetricsRegistry != nil {
		globalMetricsRegistry.Counter(name, labels...)
	}
}

func emitMetricWithContext(ctx context.Context, name string, value float64, labels ...string) {
	if globalMetricsRegistry != nil {
		globalMetricsRegistry.EmitWithContext(ctx, name, value, labels...)
	}
}

func getContextBaggage(ctx context.Context) map[string]string {
	if globalMetricsRegistry != nil {
		return globalMetricsRegistry.GetBaggage(ctx)
	}
	return make(map[string]string)
}
