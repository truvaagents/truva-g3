package main

import (
	"testing"

	"github.com/truvaagents/truva-g3/telemetry"
)

func TestResilienceTelemetryConfig(t *testing.T) {
	for _, environment := range []struct {
		name    string
		profile telemetry.Profile
	}{
		{"", telemetry.ProfileDevelopment},
		{"development", telemetry.ProfileDevelopment},
		{"staging", telemetry.ProfileStaging},
		{"qa", telemetry.ProfileStaging},
		{"production", telemetry.ProfileProduction},
		{"prod", telemetry.ProfileProduction},
	} {
		t.Run(environment.name, func(t *testing.T) {
			t.Setenv("APP_ENV", environment.name)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector.example:4318")
			config := resilienceTelemetryConfig()
			if config.ServiceName != "research-assistant-resilience" || config.Endpoint != "http://collector.example:4318" {
				t.Fatalf("unexpected telemetry identity or endpoint: %#v", config)
			}
			if config.CardinalityLimit != telemetry.UseProfile(environment.profile).CardinalityLimit {
				t.Fatal("wrong telemetry profile")
			}
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
			if resilienceTelemetryConfig().Endpoint != telemetry.UseProfile(environment.profile).Endpoint {
				t.Fatal("empty endpoint must retain the profile default")
			}
		})
	}
}
