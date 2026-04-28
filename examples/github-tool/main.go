package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type Config struct {
	Port      int
	RedisURL  string
	Namespace string
	DevMode   bool

	GitHubToken         string
	GitHubAPIBaseURL    string
	GitHubUploadBaseURL string

	ArtifactBackend string
	ArtifactTTL     time.Duration
	MaxPatchBytes   int64
	MaxFileBytes    int64
	MaxSliceBytes   int64
	MaxContextLines int
}

func main() {
	startupStart := time.Now()

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	// Tool-style initialization: set component type BEFORE telemetry init.
	core.SetCurrentComponentType(core.ComponentTypeTool)
	declareMetrics()
	initTelemetry("github-tool")
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = telemetry.Shutdown(ctx)
	}()

	redisClient, err := connectRedis(cfg.RedisURL)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer func() { _ = redisClient.Close() }()

	tool, err := NewGitHubTool(cfg, redisClient)
	if err != nil {
		log.Fatalf("create github tool: %v", err)
	}

	framework, err := core.NewFramework(tool,
		core.WithName("github-tool"),
		core.WithPort(cfg.Port),
		core.WithNamespace(cfg.Namespace),
		core.WithRedisURL(cfg.RedisURL),
		core.WithDiscovery(true, "redis"),
		core.WithCORS([]string{"*"}, true),
		core.WithDevelopmentMode(cfg.DevMode),
		// Exclude probe + discovery endpoints from tracing. /api/capabilities
		// is hit every discovery cycle by every agent — tracing it would add
		// significant volume for low signal (matches slack-tool, confluence-tool,
		// and travel-chat-agent conventions).
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("github-tool",
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		log.Fatalf("create framework: %v", err)
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		tool.Logger.Info("Shutting down github-tool gracefully", nil)
		appCancel()
	}()

	tool.Logger.Info("github-tool ready", map[string]interface{}{
		"port":              cfg.Port,
		"artifact_backend":  cfg.ArtifactBackend,
		"github_configured": tool.Client.Configured(),
		"startup_ms":        time.Since(startupStart).Milliseconds(),
	})

	if err := framework.Run(appCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("framework exited: %v", err)
	}
}

// --- Config / bootstrap helpers ---

func LoadConfig() (Config, error) {
	cfg := Config{
		Port:                envInt("PORT", 8381),
		RedisURL:            os.Getenv("REDIS_URL"),
		Namespace:           os.Getenv("NAMESPACE"),
		DevMode:             os.Getenv("DEV_MODE") == "true",
		GitHubToken:         os.Getenv("GITHUB_TOKEN"),
		GitHubAPIBaseURL:    envOrDefault("GITHUB_API_BASE_URL", "https://api.github.com"),
		GitHubUploadBaseURL: envOrDefault("GITHUB_UPLOAD_BASE_URL", "https://uploads.github.com"),
		ArtifactBackend:     envOrDefault("GITHUB_TOOL_ARTIFACT_BACKEND", "redis"),
		ArtifactTTL:         envDuration("GITHUB_TOOL_ARTIFACT_TTL", 24*time.Hour),
		MaxPatchBytes:       envInt64("GITHUB_TOOL_MAX_PATCH_BYTES", 2*1024*1024),
		MaxFileBytes:        envInt64("GITHUB_TOOL_MAX_FILE_BYTES", 1*1024*1024),
		MaxSliceBytes:       envInt64("GITHUB_TOOL_MAX_SLICE_BYTES", 128*1024),
		MaxContextLines:     envInt("GITHUB_TOOL_MAX_CONTEXT_LINES", 400),
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.RedisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable required")
	}
	if !strings.HasPrefix(c.RedisURL, "redis://") && !strings.HasPrefix(c.RedisURL, "rediss://") {
		return fmt.Errorf("invalid REDIS_URL format; expected redis:// or rediss://")
	}
	// GITHUB_TOKEN is not strictly required (read-only public PRs work without it),
	// but warn at startup if absent — the value is a strong signal.
	return nil
}

func initTelemetry(serviceName string) {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}
	var profile telemetry.Profile
	switch env {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging":
		profile = telemetry.ProfileStaging
	default:
		profile = telemetry.ProfileDevelopment
	}
	config := telemetry.UseProfile(profile)
	config.ServiceName = serviceName
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		config.Endpoint = endpoint
	}
	if err := telemetry.Initialize(config); err != nil {
		log.Printf("Warning: telemetry init failed: %v", err)
		return
	}
	telemetry.EnableFrameworkIntegration(nil)
}

func connectRedis(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return client, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
