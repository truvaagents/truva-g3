package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateOpenAPISpec_Empty(t *testing.T) {
	spec := generateOpenAPISpec("test-tool", ComponentTypeTool, nil)

	assert.Equal(t, "3.0.0", spec["openapi"])

	info := spec["info"].(map[string]interface{})
	assert.Equal(t, "test-tool", info["title"])
	assert.Contains(t, info["description"], "tool")

	// Should still have framework endpoints
	paths := spec["paths"].(map[string]interface{})
	assert.Contains(t, paths, "/api/capabilities")
	assert.Contains(t, paths, "/health")
}

func TestGenerateOpenAPISpec_WithCapabilities(t *testing.T) {
	caps := []Capability{
		{
			Name:        "get_weather",
			Description: "Gets current weather",
			Endpoint:    "/api/capabilities/get_weather",
			Type:        CapabilityTool,
			InputSummary: &SchemaSummary{
				RequiredFields: []FieldHint{
					{Name: "lat", Type: "number", Example: "35.6762", Description: "Latitude"},
					{Name: "lon", Type: "number", Example: "139.6503", Description: "Longitude"},
				},
				OptionalFields: []FieldHint{
					{Name: "units", Type: "string", Description: "Temperature units"},
				},
			},
			OutputSummary: &SchemaSummary{
				RequiredFields: []FieldHint{
					{Name: "temperature", Type: "number", Description: "Current temperature"},
					{Name: "condition", Type: "string", Description: "Weather condition"},
				},
			},
		},
	}

	spec := generateOpenAPISpec("weather-tool", ComponentTypeTool, caps)

	paths := spec["paths"].(map[string]interface{})
	assert.Contains(t, paths, "/api/capabilities/get_weather")

	weatherPath := paths["/api/capabilities/get_weather"].(map[string]interface{})
	postOp := weatherPath["post"].(map[string]interface{})
	assert.Equal(t, "get_weather", postOp["operationId"])
	assert.Equal(t, "Gets current weather", postOp["summary"])
	assert.Equal(t, []string{"tool"}, postOp["tags"])

	// Verify request body has $ref
	reqBody := postOp["requestBody"].(map[string]interface{})
	assert.True(t, reqBody["required"].(bool))
	content := reqBody["content"].(map[string]interface{})
	jsonContent := content["application/json"].(map[string]interface{})
	schema := jsonContent["schema"].(map[string]interface{})
	assert.Equal(t, "#/components/schemas/get_weatherInput", schema["$ref"])

	// Verify response has $ref
	responses := postOp["responses"].(map[string]interface{})
	resp200 := responses["200"].(map[string]interface{})
	respContent := resp200["content"].(map[string]interface{})
	respJSON := respContent["application/json"].(map[string]interface{})
	respSchema := respJSON["schema"].(map[string]interface{})
	assert.Equal(t, "#/components/schemas/get_weatherOutput", respSchema["$ref"])

	// Verify component schemas
	components := spec["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})

	inputSchema := schemas["get_weatherInput"].(map[string]interface{})
	assert.Equal(t, "object", inputSchema["type"])
	props := inputSchema["properties"].(map[string]interface{})
	assert.Contains(t, props, "lat")
	assert.Contains(t, props, "lon")
	assert.Contains(t, props, "units")
	assert.Equal(t, []string{"lat", "lon"}, inputSchema["required"])

	outputSchema := schemas["get_weatherOutput"].(map[string]interface{})
	outProps := outputSchema["properties"].(map[string]interface{})
	assert.Contains(t, outProps, "temperature")
	assert.Contains(t, outProps, "condition")
}

func TestGenerateOpenAPISpec_InternalCapabilitiesIncludedAndTagged(t *testing.T) {
	caps := []Capability{
		{
			Name:        "public_cap",
			Description: "Public capability",
			Endpoint:    "/api/capabilities/public_cap",
		},
		{
			Name:        "internal_cap",
			Description: "Internal capability",
			Endpoint:    "/internal",
			Internal:    true,
		},
	}

	spec := generateOpenAPISpec("test-agent", ComponentTypeAgent, caps)
	paths := spec["paths"].(map[string]interface{})

	// Both capabilities should be present — Internal means "hidden from LLM",
	// not "hidden from API documentation".
	assert.Contains(t, paths, "/api/capabilities/public_cap")
	assert.Contains(t, paths, "/internal")

	// Public cap should be tagged only with its type.
	publicOp := paths["/api/capabilities/public_cap"].(map[string]interface{})["post"].(map[string]interface{})
	assert.Equal(t, []string{"tool"}, publicOp["tags"])

	// Internal cap should be tagged with both its type and "internal" so users
	// can distinguish public vs internal endpoints in the UI.
	internalOp := paths["/internal"].(map[string]interface{})["post"].(map[string]interface{})
	assert.Equal(t, []string{"tool", "internal"}, internalOp["tags"])
}

func TestGenerateOpenAPISpec_DefaultEndpoint(t *testing.T) {
	caps := []Capability{
		{
			Name:        "my_capability",
			Description: "A capability with no explicit endpoint",
		},
	}

	spec := generateOpenAPISpec("test", ComponentTypeTool, caps)
	paths := spec["paths"].(map[string]interface{})
	assert.Contains(t, paths, "/api/capabilities/my_capability")
}

func TestGenerateOpenAPISpec_NoInputSummary(t *testing.T) {
	caps := []Capability{
		{
			Name:        "simple_cap",
			Description: "No schema info",
			Endpoint:    "/simple",
		},
	}

	spec := generateOpenAPISpec("test", ComponentTypeTool, caps)
	paths := spec["paths"].(map[string]interface{})
	postOp := paths["/simple"].(map[string]interface{})["post"].(map[string]interface{})

	reqBody := postOp["requestBody"].(map[string]interface{})
	content := reqBody["content"].(map[string]interface{})
	jsonContent := content["application/json"].(map[string]interface{})
	schema := jsonContent["schema"].(map[string]interface{})
	// Should be a plain object schema, not a $ref
	assert.Equal(t, "object", schema["type"])
	assert.NotContains(t, schema, "$ref")

	// No components since no schemas were generated
	assert.NotContains(t, spec, "components")
}

func TestGenerateOpenAPISpec_CapabilityTypes(t *testing.T) {
	caps := []Capability{
		{Name: "tool_cap", Type: CapabilityTool, Endpoint: "/t"},
		{Name: "reasoning_cap", Type: CapabilityReasoning, Endpoint: "/r"},
		{Name: "orch_cap", Type: CapabilityOrchestrator, Endpoint: "/o"},
		{Name: "default_cap", Endpoint: "/d"}, // empty Type defaults to "tool"
	}

	spec := generateOpenAPISpec("test", ComponentTypeAgent, caps)
	paths := spec["paths"].(map[string]interface{})

	getTag := func(path string) string {
		op := paths[path].(map[string]interface{})["post"].(map[string]interface{})
		return op["tags"].([]string)[0]
	}

	assert.Equal(t, "tool", getTag("/t"))
	assert.Equal(t, "reasoning", getTag("/r"))
	assert.Equal(t, "orchestrator", getTag("/o"))
	assert.Equal(t, "tool", getTag("/d"))
}

func TestFieldHintToOpenAPIProperty(t *testing.T) {
	tests := []struct {
		name     string
		field    FieldHint
		wantType string
		wantDesc bool
		wantEx   bool
	}{
		{
			name:     "full field",
			field:    FieldHint{Name: "lat", Type: "number", Description: "Latitude", Example: "35.6"},
			wantType: "number",
			wantDesc: true,
			wantEx:   true,
		},
		{
			name:     "minimal field",
			field:    FieldHint{Name: "id", Type: "string"},
			wantType: "string",
			wantDesc: false,
			wantEx:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := fieldHintToOpenAPIProperty(tt.field)
			assert.Equal(t, tt.wantType, prop["type"])
			if tt.wantDesc {
				assert.Contains(t, prop, "description")
			} else {
				assert.NotContains(t, prop, "description")
			}
			if tt.wantEx {
				assert.Contains(t, prop, "example")
			} else {
				assert.NotContains(t, prop, "example")
			}
		})
	}
}

func TestOpenAPIEndpoint_DisabledByDefault(t *testing.T) {
	// A tool built with the default config should NOT expose /openapi.json.
	// This is the production-safe default — see HTTPConfig.EnableOpenAPI godoc.
	tool := NewTool("test-tool")
	tool.Config = DefaultConfig()
	tool.setupStandardEndpoints()

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	tool.mux.ServeHTTP(rec, req)

	// With the endpoint unregistered, net/http's ServeMux responds 404.
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"/openapi.json should NOT be exposed by default — opt-in only")
}

func TestOpenAPIEndpoint_EnabledViaOption(t *testing.T) {
	// WithOpenAPI(true) should flip the gate on.
	cfg, err := NewConfig(WithOpenAPI(true))
	require.NoError(t, err)

	tool := NewTool("test-tool")
	tool.Config = cfg
	tool.RegisterCapability(Capability{
		Name:        "ping",
		Description: "test cap",
	})
	tool.setupStandardEndpoints()

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	tool.mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
	assert.Equal(t, "3.0.0", spec["openapi"])
}

func TestOpenAPIEndpoint_EnabledViaEnvVar(t *testing.T) {
	// TRUVAG3_ENABLE_OPENAPI=true should flip the gate on via LoadFromEnv.
	t.Setenv("TRUVAG3_ENABLE_OPENAPI", "true")

	cfg := DefaultConfig()
	require.NoError(t, cfg.LoadFromEnv())
	assert.True(t, cfg.HTTP.EnableOpenAPI,
		"TRUVAG3_ENABLE_OPENAPI=true should enable the endpoint")

	tool := NewTool("test-tool")
	tool.Config = cfg
	tool.setupStandardEndpoints()

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	tool.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestOpenAPIEndpoint_ExplicitFalseViaEnvVar(t *testing.T) {
	// TRUVAG3_ENABLE_OPENAPI=false should keep it disabled even if something
	// else had flipped it on — simple string-based parseBool roundtrip.
	t.Setenv("TRUVAG3_ENABLE_OPENAPI", "false")

	cfg := DefaultConfig()
	cfg.HTTP.EnableOpenAPI = true // simulate something pre-enabling it
	require.NoError(t, cfg.LoadFromEnv())
	assert.False(t, cfg.HTTP.EnableOpenAPI,
		"TRUVAG3_ENABLE_OPENAPI=false should override a previously-set true")
}

func TestOpenAPIEndpoint_DisabledByDefaultOnAgent(t *testing.T) {
	// Same gate applies to BaseAgent.
	agent := NewBaseAgent("test-agent")
	agent.Config = DefaultConfig()

	// Agent sets up endpoints inline in Start(); for this unit test we
	// exercise the gate condition directly since Start() spins a server.
	// The important assertion is that the default config has the gate off.
	assert.False(t, agent.Config.HTTP.EnableOpenAPI,
		"DefaultConfig() should leave EnableOpenAPI=false")
}

func TestOpenAPIHandler(t *testing.T) {
	caps := []Capability{
		{
			Name:        "test_cap",
			Description: "Test",
			Endpoint:    "/test",
		},
	}

	handler := openAPIHandler("test-svc", ComponentTypeTool, func() []Capability {
		return caps
	})

	t.Run("GET returns valid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var spec map[string]interface{}
		err := json.Unmarshal(rec.Body.Bytes(), &spec)
		require.NoError(t, err)
		assert.Equal(t, "3.0.0", spec["openapi"])
	})

	t.Run("POST returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/openapi.json", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestOpenAPIHandler_ServersURL(t *testing.T) {
	handler := openAPIHandler("svc", ComponentTypeTool, func() []Capability { return nil })

	getServers := func(req *http.Request) []map[string]interface{} {
		rec := httptest.NewRecorder()
		handler(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var spec map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
		raw, ok := spec["servers"].([]interface{})
		require.True(t, ok, "servers field missing or wrong type")
		out := make([]map[string]interface{}, 0, len(raw))
		for _, s := range raw {
			out = append(out, s.(map[string]interface{}))
		}
		return out
	}

	t.Run("falls back to r.Host when no forwarded headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Host = "weather-tool-service"
		servers := getServers(req)
		assert.Equal(t, "http://weather-tool-service", servers[0]["url"])
	})

	t.Run("uses X-Forwarded-Host when set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Host = "weather-tool-service.truvag3-examples.svc.cluster.local"
		req.Header.Set("X-Forwarded-Host", "swagger.localhost")
		servers := getServers(req)
		assert.Equal(t, "http://swagger.localhost", servers[0]["url"])
	})

	t.Run("appends X-Forwarded-Prefix to forwarded host", func(t *testing.T) {
		// This is the swagger-ui nginx proxy case: the spec is fetched at
		// http://swagger.localhost/svc/<component>/openapi.json, and the
		// rendered server URL must round-trip through that same prefix so
		// "Try it out" capability calls reach the agent.
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Host = "weather-tool-service.truvag3-examples.svc.cluster.local"
		req.Header.Set("X-Forwarded-Host", "swagger.localhost")
		req.Header.Set("X-Forwarded-Prefix", "/svc/weather-tool-service")
		servers := getServers(req)
		assert.Equal(t, "http://swagger.localhost/svc/weather-tool-service", servers[0]["url"])
	})

	t.Run("honors X-Forwarded-Proto for https", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Host = "weather-tool-service"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "apis.dev.corp")
		req.Header.Set("X-Forwarded-Prefix", "/weather-tool-v2")
		servers := getServers(req)
		assert.Equal(t, "https://apis.dev.corp/weather-tool-v2", servers[0]["url"])
	})

	t.Run("takes leftmost host from comma-separated X-Forwarded-Host", func(t *testing.T) {
		// When multiple proxies are chained, X-Forwarded-Host carries each
		// hop's view of the host as a comma-separated list. The browser's
		// original host is the leftmost entry — that's the one we want for
		// the rendered server URL, not an intermediate proxy.
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		req.Host = "weather-tool-service.truvag3-examples.svc.cluster.local"
		req.Header.Set("X-Forwarded-Host", "swagger.localhost, ingress-nginx.local")
		req.Header.Set("X-Forwarded-Prefix", "/svc/weather-tool-service")
		servers := getServers(req)
		assert.Equal(t, "http://swagger.localhost/svc/weather-tool-service", servers[0]["url"])
	})
}

func TestSchemaSummaryToJSONSchema(t *testing.T) {
	t.Run("nil summary", func(t *testing.T) {
		schema := schemaSummaryToJSONSchema(nil)
		assert.Equal(t, "object", schema["type"])
		assert.NotContains(t, schema, "properties")
	})

	t.Run("with fields", func(t *testing.T) {
		summary := &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "query", Type: "string", Description: "Search query"},
			},
			OptionalFields: []FieldHint{
				{Name: "limit", Type: "number", Description: "Max results"},
			},
		}
		schema := schemaSummaryToJSONSchema(summary)

		props := schema["properties"].(map[string]interface{})
		assert.Contains(t, props, "query")
		assert.Contains(t, props, "limit")
		assert.Equal(t, []string{"query"}, schema["required"])
	})
}

func TestGenerateOpenAPISpec_DeterministicOrder(t *testing.T) {
	caps := []Capability{
		{Name: "zebra", Endpoint: "/z"},
		{Name: "alpha", Endpoint: "/a"},
		{Name: "middle", Endpoint: "/m"},
	}

	// Generate twice and compare — order should be stable.
	spec1, _ := json.Marshal(generateOpenAPISpec("test", ComponentTypeTool, caps))
	spec2, _ := json.Marshal(generateOpenAPISpec("test", ComponentTypeTool, caps))
	assert.Equal(t, string(spec1), string(spec2))
}
