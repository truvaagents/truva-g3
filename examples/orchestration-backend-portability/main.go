package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	portableAPIService        = "portable-agent-api"
	portableWorkerService     = "portable-agent-worker"
	portableExecutorService   = "portable-scheduled-executor"
	defaultApplicationPort    = 8080
	defaultApplicationNS      = "truvag3-examples"
	defaultBackendNamespace   = "orchestration-portability"
	defaultBackendAckWait     = 2 * time.Minute
	applicationStartupTimeout = 20 * time.Second
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("orchestration portability example stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	mode := strings.ToLower(envOrDefault("TRUVAG3_MODE", "api"))
	serviceName, componentType, err := modeIdentity(mode)
	if err != nil {
		return err
	}
	port, err := applicationPort()
	if err != nil {
		return err
	}
	config, err := loadConfig()
	if err != nil {
		return err
	}

	core.SetCurrentComponentType(componentType)
	initTelemetry(serviceName)
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(shutdownContext); err != nil {
			log.Printf("telemetry shutdown warning: %v", err)
		}
	}()

	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	startupContext, cancel := context.WithTimeout(rootContext, applicationStartupTimeout)
	defer cancel()
	switch mode {
	case "api":
		backends, owner, err := buildAPIBackends(startupContext, config)
		if err != nil {
			return err
		}
		defer closeBackends(owner)
		logDescriptor(mode, backends.Descriptor)
		return runAPI(rootContext, config, backends, port)
	case "worker":
		backends, owner, err := buildWorkerBackends(startupContext, config)
		if err != nil {
			return err
		}
		defer closeBackends(owner)
		logDescriptor(mode, backends.Descriptor)
		return runWorker(rootContext, config, backends, port)
	case "scheduler":
		backends, owner, err := buildSchedulerBackends(startupContext, config)
		if err != nil {
			return err
		}
		defer closeBackends(owner)
		logDescriptor(mode, backends.Descriptor)
		return RunSchedulerTool(
			rootContext,
			backends,
			config.RedisURL,
			port,
			envOrDefault("NAMESPACE", defaultApplicationNS),
		)
	case "scheduled-executor":
		backends, owner, err := buildExecutorBackends(startupContext, config)
		if err != nil {
			return err
		}
		defer closeBackends(owner)
		logDescriptor(mode, backends.Descriptor)
		return runScheduledExecutor(rootContext, config, backends, port)
	case "target-agent":
		return runTargetAgent(rootContext, config, port)
	default:
		return fmt.Errorf("unsupported TRUVAG3_MODE %q", mode)
	}
}

func runAPI(ctx context.Context, config Config, backends APIBackends, port int) error {
	agent, framework, err := newAgentFramework(portableAPIService, config, port, false)
	if err != nil {
		return err
	}
	api, err := NewAPI(backends)
	if err != nil {
		return err
	}
	if err := api.RegisterHandlers(agent); err != nil {
		return err
	}
	agent.Logger.Info("portable API starting", map[string]interface{}{"port": port})
	return framework.Run(ctx)
}

func runWorker(ctx context.Context, config Config, backends WorkerBackends, port int) error {
	agent, framework, err := newAgentFramework(portableWorkerService, config, port, false)
	if err != nil {
		return err
	}
	worker, err := NewWorker(backends, agent.Logger)
	if err != nil {
		return err
	}
	framework.RegisterRunnable(worker)
	agent.Logger.Info("portable worker framework starting", map[string]interface{}{"port": port})
	return framework.Run(ctx)
}

func runScheduledExecutor(ctx context.Context, config Config, backends ExecutorBackends, port int) error {
	agent, framework, err := newAgentFramework(portableExecutorService, config, port, false)
	if err != nil {
		return err
	}
	executor, err := NewScheduledExecutor(
		backends,
		&http.Client{Timeout: 90 * time.Second},
		agent.Logger,
	)
	if err != nil {
		return err
	}
	framework.RegisterRunnable(executor)
	agent.Logger.Info("portable scheduled executor framework starting", map[string]interface{}{"port": port})
	return framework.Run(ctx)
}

func newAgentFramework(
	serviceName string,
	config Config,
	port int,
	discoveryEnabled bool,
) (*core.BaseAgent, *core.Framework, error) {
	agent := core.NewBaseAgent(serviceName)
	framework, err := core.NewFramework(
		agent,
		core.WithName(serviceName),
		core.WithPort(port),
		core.WithNamespace(envOrDefault("NAMESPACE", defaultApplicationNS)),
		core.WithRedisURL(config.RedisURL),
		core.WithDiscovery(discoveryEnabled, "redis"),
		core.WithCORSDefaults(),
		core.WithDevelopmentMode(strings.EqualFold(envOrDefault("DEV_MODE", "false"), "true")),
		core.WithMiddleware(telemetry.TracingMiddlewareWithConfig(
			serviceName,
			&telemetry.TracingMiddlewareConfig{
				ExcludedPaths: []string{"/health", "/metrics", "/ready", "/api/capabilities"},
			},
		)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("construct %s framework: %w", serviceName, err)
	}
	return agent, framework, nil
}

func modeIdentity(mode string) (string, core.ComponentType, error) {
	switch mode {
	case "api":
		return portableAPIService, core.ComponentTypeAgent, nil
	case "worker":
		return portableWorkerService, core.ComponentTypeAgent, nil
	case "scheduler":
		return SchedulerServiceName, core.ComponentTypeTool, nil
	case "scheduled-executor":
		return portableExecutorService, core.ComponentTypeAgent, nil
	case "target-agent":
		return portableTargetService, core.ComponentTypeAgent, nil
	default:
		return "", "", fmt.Errorf("TRUVAG3_MODE must be api, worker, scheduler, scheduled-executor, or target-agent")
	}
}

func loadConfig() (Config, error) {
	ackWait, err := time.ParseDuration(envOrDefault("PORTABILITY_ACK_WAIT", defaultBackendAckWait.String()))
	if err != nil || ackWait <= 0 {
		return Config{}, fmt.Errorf("PORTABILITY_ACK_WAIT must be a positive duration")
	}
	return Config{
		PostgresURL: os.Getenv("POSTGRES_URL"),
		NATSURL:     os.Getenv("NATS_URL"),
		RedisURL:    os.Getenv("REDIS_URL"),
		Namespace:   envOrDefault("PORTABILITY_BACKEND_NAMESPACE", defaultBackendNamespace),
		Queue:       envOrDefault("PORTABILITY_QUEUE", DefaultQueue),
		WorkflowID:  envOrDefault("PORTABILITY_WORKFLOW_ID", DefaultWorkflowID),
		AckWait:     ackWait,
	}, nil
}

func applicationPort() (int, error) {
	port, err := strconv.Atoi(envOrDefault("PORT", strconv.Itoa(defaultApplicationPort)))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("PORT must be an integer between 1 and 65535")
	}
	return port, nil
}

func initTelemetry(serviceName string) {
	environment := strings.ToLower(envOrDefault("APP_ENV", "development"))
	profile := telemetry.ProfileDevelopment
	switch environment {
	case "production", "prod":
		profile = telemetry.ProfileProduction
	case "staging", "stage", "qa":
		profile = telemetry.ProfileStaging
	}
	config := telemetry.UseProfile(profile)
	config.ServiceName = serviceName
	if endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); endpoint != "" {
		config.Endpoint = endpoint
	}
	if err := telemetry.Initialize(config); err != nil {
		log.Printf("telemetry initialization warning: %v", err)
		return
	}
	telemetry.EnableFrameworkIntegration(nil)
}

func closeBackends(owner *backendOwner) {
	if err := owner.Close(); err != nil {
		log.Printf("backend shutdown warning: %v", err)
	}
}

func logDescriptor(mode string, descriptor BackendDescriptor) {
	log.Printf("mixed backend composition validated: mode=%s backends=%v", mode, descriptor.SelectedBackends)
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
