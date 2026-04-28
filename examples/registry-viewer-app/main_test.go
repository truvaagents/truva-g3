package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/memory"
)

// newInMemoryBackendFactory returns a MemoryBackendFactory backed by the
// framework's in-memory implementations. Test-only — kept in *_test.go so
// the shipping binary never compiles this path (aligns with the storage
// abstraction proposal's "illustrative, not a shipping deliverable" note
// for Step B.4).
func newInMemoryBackendFactory(t *testing.T) MemoryBackendFactory {
	t.Helper()
	return func(domain string) (MemoryBackends, error) {
		episodic := memory.NewInMemoryEpisodicMemory(domain, 1024)
		coordinator := memory.NewInMemoryInvestigationCoordinator()
		digest, err := memory.NewInMemoryDigestCache()
		if err != nil {
			return MemoryBackends{}, err
		}
		activity, err := memory.NewInMemoryActivityCoordinator(domain)
		if err != nil {
			return MemoryBackends{}, err
		}
		return MemoryBackends{
			Episodic:    episodic,
			Coordinator: coordinator,
			Digest:      digest,
			Activity:    activity,
		}, nil
	}
}

func resetDiscoveryState() {
	discoveryClientMu.Lock()
	discoveryClient = nil
	discoveryClientMu.Unlock()
	servicesCache.Store(nil)
}

// disableMockMode flips the useMock flag off for the duration of a test.
// The viewer defaults useMock=true so that `go run .` produces a usable
// demo without Redis; unit tests need the real data path instead.
func disableMockMode(t *testing.T) {
	t.Helper()
	prev := useMock
	useMock = false
	t.Cleanup(func() { useMock = prev })
}

func resetMemoryState() {
	memoryBackendFactoryMu.Lock()
	memoryBackendFactory = nil
	memoryBackendFactoryMu.Unlock()
	memoryDomains = nil
	memoryDomainsList = nil
	memoryEnabled = false
}

// TestDiscoveryInjection wires a core.MockDiscovery into the viewer's
// discovery singleton and verifies /api/services returns the mock's data
// through the adapter layer. This proves the discovery abstraction is
// really an abstraction — any core.Discovery implementation works.
func TestDiscoveryInjection(t *testing.T) {
	disableMockMode(t)
	t.Cleanup(resetDiscoveryState)
	resetDiscoveryState()

	mock := core.NewMockDiscovery()
	ctx := context.Background()

	if err := mock.Register(ctx, &core.ServiceInfo{
		ID:          "weather-tool-1",
		Name:        "weather-tool",
		Type:        core.ComponentTypeTool,
		Description: "Weather forecasts",
		Address:     "10.0.0.1",
		Port:        8080,
		Health:      core.HealthHealthy,
		LastSeen:    time.Now(),
		Capabilities: []core.Capability{
			{
				Name:        "get-forecast",
				Description: "Fetch a forecast",
				Endpoint:    "/api/capabilities/get-forecast",
				InputTypes:  []string{"application/json"},
				OutputTypes: []string{"application/json"},
				Internal:    false,
				InputSummary: &core.SchemaSummary{
					RequiredFields: []core.FieldHint{
						{Name: "location", Type: "string", Example: "London", Description: "City"},
					},
					OptionalFields: []core.FieldHint{
						{Name: "units", Type: "string", Example: "metric"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("mock.Register: %v", err)
	}
	if err := mock.Register(ctx, &core.ServiceInfo{
		ID:       "travel-agent-1",
		Name:     "travel-agent",
		Type:     core.ComponentTypeAgent,
		Address:  "10.0.0.2",
		Port:     8090,
		Health:   core.HealthHealthy,
		LastSeen: time.Now(),
	}); err != nil {
		t.Fatalf("mock.Register: %v", err)
	}

	// Inject the mock into the viewer's singleton slot. The nil-check in
	// getDiscovery() skips construction when discoveryClient is already set.
	discoveryClientMu.Lock()
	discoveryClient = mock
	discoveryClientMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/services", nil)
	rec := httptest.NewRecorder()
	handleServices(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp RegistryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.TotalCount != 2 {
		t.Errorf("TotalCount=%d want 2", resp.TotalCount)
	}
	if resp.ToolCount != 1 || resp.AgentCount != 1 {
		t.Errorf("tool/agent=%d/%d want 1/1", resp.ToolCount, resp.AgentCount)
	}

	// handleServices sorts agents first, then by name.
	if len(resp.Services) != 2 {
		t.Fatalf("len(Services)=%d want 2", len(resp.Services))
	}
	if resp.Services[0].Type != "agent" || resp.Services[0].Name != "travel-agent" {
		t.Errorf("Services[0]=%+v want agent/travel-agent", resp.Services[0])
	}

	tool := resp.Services[1]
	if tool.ID != "weather-tool-1" || tool.Name != "weather-tool" {
		t.Errorf("tool id/name=%q/%q", tool.ID, tool.Name)
	}
	if tool.Type != "tool" {
		t.Errorf("tool.Type=%q want 'tool' (core.ComponentTypeTool must marshal as plain string)", tool.Type)
	}
	if tool.Health != "healthy" {
		t.Errorf("tool.Health=%q want 'healthy'", tool.Health)
	}
	if len(tool.Capabilities) != 1 {
		t.Fatalf("Capabilities=%d want 1", len(tool.Capabilities))
	}
	capability := tool.Capabilities[0]
	if capability.Name != "get-forecast" || capability.Endpoint != "/api/capabilities/get-forecast" {
		t.Errorf("capability name/endpoint=%q/%q", capability.Name, capability.Endpoint)
	}
	if capability.InputSummary == nil {
		t.Fatal("InputSummary should have been adapted from core.SchemaSummary, got nil")
	}
	if len(capability.InputSummary.Required) != 1 || capability.InputSummary.Required[0].Name != "location" {
		t.Errorf("Required=%+v want one hint with Name=location", capability.InputSummary.Required)
	}
	if capability.InputSummary.Required[0].Example != "London" {
		t.Errorf("adaptFieldHints dropped Example field: got %q want 'London'", capability.InputSummary.Required[0].Example)
	}
}

// TestMemoryInjection wires an in-memory MemoryBackendFactory, seeds data
// through the four write methods on the core interfaces, then verifies all
// five memory HTTP handlers return that seeded data. This proves the
// memory decoupling is real: every read path goes through core.*
// interfaces, so any impl (Redis, in-memory, anything else) plugs in by
// swapping the factory.
func TestMemoryInjection(t *testing.T) {
	disableMockMode(t)
	t.Cleanup(resetMemoryState)
	resetMemoryState()

	const domain = "infrastructure"
	factory := newInMemoryBackendFactory(t)

	memoryDomainsList = []string{domain}
	memoryEnabled = true
	memoryBackendFactoryMu.Lock()
	memoryBackendFactory = factory
	memoryBackendFactoryMu.Unlock()

	initMemoryClients(factory, []string{domain})

	dm := getMemoryDomain(domain)
	if dm == nil {
		t.Fatal("getMemoryDomain returned nil after initMemoryClients — factory wiring is broken")
	}

	ctx := context.Background()

	// Seed episodic events.
	if err := dm.episodic.RecordEvent(ctx, core.AgentEvent{
		AgentName:   "devops-chat-agent",
		AgentDomain: domain,
		ActionType:  "pod_restart",
		EntityType:  "pod",
		EntityID:    "product-catalog-7f",
		Summary:     "restarted pod product-catalog-7f",
		Outcome:     "success",
		Timestamp:   time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := dm.episodic.RecordEvent(ctx, core.AgentEvent{
		AgentName:   "devops-chat-agent",
		AgentDomain: domain,
		ActionType:  "alert_ack",
		EntityType:  "alert",
		EntityID:    "HighCPU-web-42",
		Summary:     "acknowledged HighCPU alert",
		Outcome:     "success",
		Timestamp:   time.Now().Add(-90 * time.Second),
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	// Seed an active investigation claim.
	claimed, _, err := dm.coordinator.ClaimInvestigation(ctx, "devops-chat-agent", "pod-xyz", 5*time.Minute)
	if err != nil || !claimed {
		t.Fatalf("ClaimInvestigation: claimed=%v err=%v", claimed, err)
	}

	// Seed a digest blob matching the shape handleMemoryDigest unmarshals.
	digestBytes, _ := json.Marshal(map[string]string{
		"content":       "last 24h: 2 incidents resolved",
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"last_event_ts": time.Now().UTC().Format(time.RFC3339),
	})
	if err := dm.digest.SetDigest(ctx, domain, digestBytes, time.Hour); err != nil {
		t.Fatalf("SetDigest: %v", err)
	}

	// Seed a live activity signal.
	if err := dm.activity.AnnounceActivity(ctx, core.ActivitySignal{
		AgentName:   "devops-chat-agent",
		AgentDomain: domain,
		RequestID:   "req-42",
		Query:       "why is product-catalog failing?",
		Status:      "executing",
		StartedAt:   time.Now(),
		TTL:         5 * time.Minute,
	}); err != nil {
		t.Fatalf("AnnounceActivity: %v", err)
	}

	t.Run("events", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory/events?domain="+domain, nil)
		rec := httptest.NewRecorder()
		handleMemoryEvents(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Events     []map[string]interface{} `json:"events"`
			TotalCount int                      `json:"total_count"`
			Domain     string                   `json:"domain"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.TotalCount != 2 || len(resp.Events) != 2 {
			t.Errorf("total/len=%d/%d want 2/2", resp.TotalCount, len(resp.Events))
		}
		if resp.Domain != domain {
			t.Errorf("domain=%q want %q", resp.Domain, domain)
		}
	})

	t.Run("events_recent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory/events/recent?domain="+domain+"&since=1h&limit=10", nil)
		rec := httptest.NewRecorder()
		handleMemoryEventsRecent(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			TotalCount int `json:"total_count"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.TotalCount != 2 {
			t.Errorf("TotalCount=%d want 2", resp.TotalCount)
		}
	})

	t.Run("investigations", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory/investigations?domain="+domain, nil)
		rec := httptest.NewRecorder()
		handleMemoryInvestigations(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Investigations []struct {
				EntityID string `json:"entity_id"`
				Holder   string `json:"holder"`
				Domain   string `json:"domain"`
			} `json:"investigations"`
			TotalCount int `json:"total_count"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.TotalCount != 1 || len(resp.Investigations) != 1 {
			t.Fatalf("total/len=%d/%d want 1/1", resp.TotalCount, len(resp.Investigations))
		}
		inv := resp.Investigations[0]
		if inv.EntityID != "pod-xyz" || inv.Holder != "devops-chat-agent" || inv.Domain != domain {
			t.Errorf("investigation=%+v", inv)
		}
	})

	t.Run("digest", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory/digest?domain="+domain, nil)
		rec := httptest.NewRecorder()
		handleMemoryDigest(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Domain    string `json:"domain"`
			Digest    string `json:"digest"`
			Available bool   `json:"available"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.Available {
			t.Errorf("available=false, want true (digest content should be present)")
		}
		if resp.Digest != "last 24h: 2 incidents resolved" {
			t.Errorf("digest=%q", resp.Digest)
		}
	})

	t.Run("activities", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/memory/activities?domain="+domain, nil)
		rec := httptest.NewRecorder()
		handleMemoryActivities(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Activities []struct {
				RequestID   string `json:"request_id"`
				AgentDomain string `json:"agent_domain"`
				Status      string `json:"status"`
			} `json:"activities"`
			TotalCount int `json:"total_count"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.TotalCount != 1 || len(resp.Activities) != 1 {
			t.Fatalf("total/len=%d/%d want 1/1", resp.TotalCount, len(resp.Activities))
		}
		act := resp.Activities[0]
		if act.RequestID != "req-42" || act.AgentDomain != domain || act.Status != "executing" {
			t.Errorf("activity=%+v", act)
		}
	})
}
