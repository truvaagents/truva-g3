package core

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for Config.checkObservabilityIdentityAlignment — the startup advisory
// that WARN-logs when pod app label disagrees with cfg.Name or
// cfg.Telemetry.ServiceName. Must never fail startup; must be a no-op when the
// pod label env var is not present.

func TestIdentityAlignment_NoOpWhenPodLabelUnset(t *testing.T) {
	logger := &captureLogger{}
	cfg := &Config{
		Name:   "hotel-tool",
		logger: logger,
		Telemetry: TelemetryConfig{
			ServiceName: "hotel-service", // deliberate drift — should still be silent
		},
		// Kubernetes.PodAppLabel intentionally empty
	}

	cfg.checkObservabilityIdentityAlignment()

	assert.Empty(t, logger.entries, "no pod label present → check must be a no-op regardless of other drift")
}

func TestIdentityAlignment_SilentWhenAllAgree(t *testing.T) {
	logger := &captureLogger{}
	cfg := &Config{
		Name:   "hotel-tool",
		logger: logger,
		Telemetry: TelemetryConfig{
			ServiceName: "hotel-tool",
		},
		Kubernetes: KubernetesConfig{
			PodAppLabel: "hotel-tool",
		},
	}

	cfg.checkObservabilityIdentityAlignment()

	assert.Empty(t, logger.entries, "all three identity strings agree → no warning")
}

func TestIdentityAlignment_WarnsOnNameDrift(t *testing.T) {
	// Isolate name-only drift: Telemetry.ServiceName matches the pod label,
	// only cfg.Name disagrees.
	logger := &captureLogger{}
	cfg := &Config{
		Name:   "stock-market-tool",
		logger: logger,
		Telemetry: TelemetryConfig{
			ServiceName: "stock-tool",
		},
		Kubernetes: KubernetesConfig{
			PodAppLabel: "stock-tool",
		},
	}

	cfg.checkObservabilityIdentityAlignment()

	if assert.Len(t, logger.entries, 1, "one warning expected for name drift") {
		entry := logger.entries[0]
		assert.Equal(t, "warn", entry.level)
		assert.Equal(t, "observability_identity_check", entry.fields["operation"])
		assert.Equal(t, "stock-tool", entry.fields["pod_app_label"])
		assert.Equal(t, "stock-market-tool", entry.fields["framework_name"])
		details, _ := entry.fields["drift_details"].([]string)
		if assert.Len(t, details, 1) {
			assert.Contains(t, details[0], "framework name")
			assert.Contains(t, details[0], "stock-market-tool")
			assert.Contains(t, details[0], "stock-tool")
		}
	}
}

func TestIdentityAlignment_WarnsOnTelemetryDrift(t *testing.T) {
	logger := &captureLogger{}
	cfg := &Config{
		Name:   "hotel-tool",
		logger: logger,
		Telemetry: TelemetryConfig{
			ServiceName: "hotel-service",
		},
		Kubernetes: KubernetesConfig{
			PodAppLabel: "hotel-tool",
		},
	}

	cfg.checkObservabilityIdentityAlignment()

	if assert.Len(t, logger.entries, 1, "one warning expected for telemetry drift") {
		entry := logger.entries[0]
		assert.Equal(t, "warn", entry.level)
		details, _ := entry.fields["drift_details"].([]string)
		if assert.Len(t, details, 1) {
			assert.Contains(t, details[0], "telemetry service name")
			assert.Contains(t, details[0], "hotel-service")
		}
	}
}

func TestIdentityAlignment_WarnsOnBothDrifts(t *testing.T) {
	logger := &captureLogger{}
	cfg := &Config{
		Name:   "stock-market-tool",
		logger: logger,
		Telemetry: TelemetryConfig{
			ServiceName: "stock-service",
		},
		Kubernetes: KubernetesConfig{
			PodAppLabel: "stock-tool",
		},
	}

	cfg.checkObservabilityIdentityAlignment()

	if assert.Len(t, logger.entries, 1, "a single warning should enumerate both drift axes") {
		entry := logger.entries[0]
		details, _ := entry.fields["drift_details"].([]string)
		assert.Len(t, details, 2, "both drifts reported in one warning")
	}
}

func TestIdentityAlignment_SilentWhenNameEmpty(t *testing.T) {
	// cfg.Name can legitimately be empty (user hasn't called core.WithName
	// yet and hasn't set TRUVAG3_AGENT_NAME). The check should not conjure a
	// drift warning from an empty string.
	logger := &captureLogger{}
	cfg := &Config{
		logger: logger,
		Telemetry: TelemetryConfig{
			ServiceName: "hotel-tool",
		},
		Kubernetes: KubernetesConfig{
			PodAppLabel: "hotel-tool",
		},
	}

	cfg.checkObservabilityIdentityAlignment()

	assert.Empty(t, logger.entries, "empty cfg.Name must not count as drift")
}

func TestIdentityAlignment_SilentWhenTelemetryEmpty(t *testing.T) {
	logger := &captureLogger{}
	cfg := &Config{
		Name:      "hotel-tool",
		logger:    logger,
		Telemetry: TelemetryConfig{ /* ServiceName empty */ },
		Kubernetes: KubernetesConfig{
			PodAppLabel: "hotel-tool",
		},
	}

	cfg.checkObservabilityIdentityAlignment()

	assert.Empty(t, logger.entries, "empty telemetry service name must not count as drift (defaults to cfg.Name)")
}

func TestIdentityAlignment_SilentWhenLoggerNil(t *testing.T) {
	// No logger means no one to warn. Check must still not panic.
	cfg := &Config{
		Name: "mismatch-a",
		Telemetry: TelemetryConfig{
			ServiceName: "mismatch-b",
		},
		Kubernetes: KubernetesConfig{
			PodAppLabel: "mismatch-c",
		},
		// logger nil
	}

	assert.NotPanics(t, func() {
		cfg.checkObservabilityIdentityAlignment()
	})
}

// TestLoadFromEnv_PodAppLabel verifies that TRUVAG3_K8S_POD_APP_LABEL is
// correctly loaded into Kubernetes.PodAppLabel when the framework detects it
// is running in Kubernetes (via KUBERNETES_SERVICE_HOST). Guards against
// silent removal of the env-var wiring added alongside the identity-drift
// check.
func TestLoadFromEnv_PodAppLabel(t *testing.T) {
	t.Run("loads when running in Kubernetes", func(t *testing.T) {
		envVars := map[string]string{
			"KUBERNETES_SERVICE_HOST":   "10.96.0.1",
			"TRUVAG3_K8S_POD_APP_LABEL": "hotel-tool",
		}
		for k, v := range envVars {
			require.NoError(t, os.Setenv(k, v))
			k := k
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}

		cfg := DefaultConfig()
		require.NoError(t, cfg.LoadFromEnv())

		assert.Equal(t, "hotel-tool", cfg.Kubernetes.PodAppLabel)
		assert.True(t, cfg.Kubernetes.Enabled)
	})

	t.Run("stays empty outside Kubernetes even if env var is set", func(t *testing.T) {
		// Defensive: the check is a no-op when PodAppLabel is empty, so
		// loading it outside K8s would be harmless — but the current
		// convention matches sibling fields (PodName, NodeName) which all
		// only load inside the K8s-detection block. Document that contract.
		require.NoError(t, os.Unsetenv("KUBERNETES_SERVICE_HOST"))
		require.NoError(t, os.Setenv("TRUVAG3_K8S_POD_APP_LABEL", "hotel-tool"))
		t.Cleanup(func() { _ = os.Unsetenv("TRUVAG3_K8S_POD_APP_LABEL") })

		cfg := DefaultConfig()
		require.NoError(t, cfg.LoadFromEnv())

		assert.Empty(t, cfg.Kubernetes.PodAppLabel,
			"env var is only honored inside the K8s-detection block (matches PodName, NodeName convention)")
	})
}
