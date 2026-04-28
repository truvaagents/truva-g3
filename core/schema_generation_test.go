package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// ========== Phase 3: Schema Generation Tests ==========

// TestGenerateJSONSchema_WithInputSummary tests schema generation with complete InputSummary
func TestGenerateJSONSchema_WithInputSummary(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "weather",
		Description: "Get current weather conditions",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{
					Name:        "location",
					Type:        "string",
					Example:     "London",
					Description: "City name or coordinates",
				},
				{
					Name:        "country",
					Type:        "string",
					Example:     "UK",
					Description: "Country code",
				},
			},
			OptionalFields: []FieldHint{
				{
					Name:        "units",
					Type:        "string",
					Example:     "metric",
					Description: "Temperature unit: metric or imperial",
				},
			},
		},
	}

	schema := agent.generateJSONSchema(cap)

	// Verify schema structure
	if schema["$schema"] != "http://json-schema.org/draft-07/schema#" {
		t.Errorf("Expected $schema to be JSON Schema Draft 07, got %v", schema["$schema"])
	}

	if schema["type"] != "object" {
		t.Errorf("Expected type to be 'object', got %v", schema["type"])
	}

	if schema["title"] != "weather" {
		t.Errorf("Expected title to be 'weather', got %v", schema["title"])
	}

	if schema["description"] != "Get current weather conditions" {
		t.Errorf("Expected description to match capability description")
	}

	// Verify properties exist
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected properties to be a map")
	}

	if len(properties) != 3 {
		t.Errorf("Expected 3 properties (location, country, units), got %d", len(properties))
	}

	// Verify required fields
	requiredFields, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("Expected required to be a string slice")
	}

	if len(requiredFields) != 2 {
		t.Errorf("Expected 2 required fields, got %d", len(requiredFields))
	}

	expectedRequired := map[string]bool{"location": true, "country": true}
	for _, field := range requiredFields {
		if !expectedRequired[field] {
			t.Errorf("Unexpected required field: %s", field)
		}
	}

	// Verify additionalProperties is false
	if schema["additionalProperties"] != false {
		t.Errorf("Expected additionalProperties to be false, got %v", schema["additionalProperties"])
	}

	// Verify property details for location
	locationProp, ok := properties["location"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected location property to be a map")
	}

	if locationProp["type"] != "string" {
		t.Errorf("Expected location type to be 'string', got %v", locationProp["type"])
	}

	if locationProp["description"] != "City name or coordinates" {
		t.Errorf("Expected location description to match")
	}

	examples, ok := locationProp["examples"].([]string)
	if !ok {
		t.Fatal("Expected examples to be a string slice")
	}

	if len(examples) != 1 || examples[0] != "London" {
		t.Errorf("Expected examples to contain 'London', got %v", examples)
	}
}

// TestGenerateJSONSchema_WithoutInputSummary tests minimal schema generation
func TestGenerateJSONSchema_WithoutInputSummary(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "simple",
		Description: "Simple capability without schema",
	}

	schema := agent.generateJSONSchema(cap)

	// Should return minimal schema
	if schema["$schema"] != "http://json-schema.org/draft-07/schema#" {
		t.Errorf("Expected $schema even without InputSummary")
	}

	if schema["type"] != "object" {
		t.Errorf("Expected type to be 'object'")
	}

	if schema["title"] != "simple" {
		t.Errorf("Expected title to be 'simple'")
	}

	// Should not have properties or required fields
	if schema["properties"] != nil {
		t.Error("Should not have properties when InputSummary is nil")
	}

	if schema["required"] != nil {
		t.Error("Should not have required fields when InputSummary is nil")
	}
}

// TestGenerateJSONSchema_OnlyRequiredFields tests schema with only required fields
func TestGenerateJSONSchema_OnlyRequiredFields(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "validate",
		Description: "Validation capability",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "id", Type: "number"},
				{Name: "name", Type: "string"},
			},
		},
	}

	schema := agent.generateJSONSchema(cap)

	properties := schema["properties"].(map[string]interface{})
	if len(properties) != 2 {
		t.Errorf("Expected 2 properties, got %d", len(properties))
	}

	required := schema["required"].([]string)
	if len(required) != 2 {
		t.Errorf("Expected 2 required fields, got %d", len(required))
	}
}

// TestGenerateJSONSchema_OnlyOptionalFields tests schema with only optional fields
func TestGenerateJSONSchema_OnlyOptionalFields(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "optional_test",
		Description: "Test optional fields",
		InputSummary: &SchemaSummary{
			OptionalFields: []FieldHint{
				{Name: "page", Type: "number"},
				{Name: "limit", Type: "number"},
			},
		},
	}

	schema := agent.generateJSONSchema(cap)

	properties := schema["properties"].(map[string]interface{})
	if len(properties) != 2 {
		t.Errorf("Expected 2 properties, got %d", len(properties))
	}

	// Should not have required array (or it should be empty)
	if required, ok := schema["required"].([]string); ok && len(required) > 0 {
		t.Errorf("Expected no required fields, got %v", required)
	}
}

// TestGenerateJSONSchema_EmptyInputSummary tests schema with empty InputSummary
func TestGenerateJSONSchema_EmptyInputSummary(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:         "empty_schema",
		Description:  "Empty schema test",
		InputSummary: &SchemaSummary{},
	}

	schema := agent.generateJSONSchema(cap)

	// Should have basic structure but no properties
	if schema["$schema"] != "http://json-schema.org/draft-07/schema#" {
		t.Error("Expected $schema to be set")
	}

	properties := schema["properties"].(map[string]interface{})
	if len(properties) != 0 {
		t.Errorf("Expected no properties, got %d", len(properties))
	}
}

// ========== fieldHintToJSONSchema Tests ==========

// TestFieldHintToJSONSchema_AllFields tests conversion with all fields populated
func TestFieldHintToJSONSchema_AllFields(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	hint := FieldHint{
		Name:        "username",
		Type:        "string",
		Example:     "john_doe",
		Description: "User's login name",
	}

	prop := agent.fieldHintToJSONSchema(hint)

	if prop["type"] != "string" {
		t.Errorf("Expected type 'string', got %v", prop["type"])
	}

	if prop["description"] != "User's login name" {
		t.Errorf("Expected description to match")
	}

	examples, ok := prop["examples"].([]string)
	if !ok {
		t.Fatal("Expected examples to be a string slice")
	}

	if len(examples) != 1 || examples[0] != "john_doe" {
		t.Errorf("Expected examples to contain 'john_doe', got %v", examples)
	}
}

// TestFieldHintToJSONSchema_TypeOnly tests conversion with only type field
func TestFieldHintToJSONSchema_TypeOnly(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	hint := FieldHint{
		Name: "count",
		Type: "number",
	}

	prop := agent.fieldHintToJSONSchema(hint)

	if prop["type"] != "number" {
		t.Errorf("Expected type 'number', got %v", prop["type"])
	}

	// Should not have description or examples
	if prop["description"] != nil {
		t.Error("Should not have description when not provided")
	}

	if prop["examples"] != nil {
		t.Error("Should not have examples when not provided")
	}
}

// TestFieldHintToJSONSchema_WithDescription tests conversion with description but no example
func TestFieldHintToJSONSchema_WithDescription(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	hint := FieldHint{
		Name:        "status",
		Type:        "boolean",
		Description: "Active status flag",
	}

	prop := agent.fieldHintToJSONSchema(hint)

	if prop["type"] != "boolean" {
		t.Errorf("Expected type 'boolean', got %v", prop["type"])
	}

	if prop["description"] != "Active status flag" {
		t.Errorf("Expected description to match")
	}

	// Should not have examples
	if prop["examples"] != nil {
		t.Error("Should not have examples when not provided")
	}
}

// TestFieldHintToJSONSchema_WithExample tests conversion with example but no description
func TestFieldHintToJSONSchema_WithExample(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	hint := FieldHint{
		Name:    "color",
		Type:    "string",
		Example: "blue",
	}

	prop := agent.fieldHintToJSONSchema(hint)

	if prop["type"] != "string" {
		t.Errorf("Expected type 'string', got %v", prop["type"])
	}

	// Should not have description
	if prop["description"] != nil {
		t.Error("Should not have description when not provided")
	}

	examples := prop["examples"].([]string)
	if len(examples) != 1 || examples[0] != "blue" {
		t.Errorf("Expected examples to contain 'blue', got %v", examples)
	}
}

// TestFieldHintToJSONSchema_DifferentTypes tests conversion with different JSON types
func TestFieldHintToJSONSchema_DifferentTypes(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	tests := []struct {
		name         string
		hint         FieldHint
		expectedType string
	}{
		{
			name:         "string type",
			hint:         FieldHint{Name: "text", Type: "string"},
			expectedType: "string",
		},
		{
			name:         "number type",
			hint:         FieldHint{Name: "count", Type: "number"},
			expectedType: "number",
		},
		{
			name:         "boolean type",
			hint:         FieldHint{Name: "active", Type: "boolean"},
			expectedType: "boolean",
		},
		{
			name:         "object type",
			hint:         FieldHint{Name: "config", Type: "object"},
			expectedType: "object",
		},
		{
			name:         "array type",
			hint:         FieldHint{Name: "items", Type: "array"},
			expectedType: "array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := agent.fieldHintToJSONSchema(tt.hint)

			if prop["type"] != tt.expectedType {
				t.Errorf("Expected type '%s', got %v", tt.expectedType, prop["type"])
			}
		})
	}
}

// ========== handleSchemaRequest Tests ==========

// TestHandleSchemaRequest_GET tests successful GET request
func TestHandleSchemaRequest_GET(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "test_capability",
		Description: "Test capability for schema endpoint",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "field1", Type: "string"},
			},
		},
	}

	handler := agent.handleSchemaRequest(cap)

	req := httptest.NewRequest("GET", "/api/capabilities/test_capability/schema", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	// Verify status code
	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	// Verify content type
	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
	}

	// Verify response is valid JSON
	var schema map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &schema); err != nil {
		t.Errorf("Failed to parse JSON response: %v", err)
	}

	// Verify schema structure
	if schema["$schema"] != "http://json-schema.org/draft-07/schema#" {
		t.Error("Expected valid JSON Schema in response")
	}

	if schema["title"] != "test_capability" {
		t.Error("Expected schema title to match capability name")
	}
}

// TestHandleSchemaRequest_POST tests that POST is not allowed
func TestHandleSchemaRequest_POST(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{Name: "test"}
	handler := agent.handleSchemaRequest(cap)

	req := httptest.NewRequest("POST", "/schema", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405 for POST, got %d", rec.Code)
	}
}

// TestHandleSchemaRequest_PUT tests that PUT is not allowed
func TestHandleSchemaRequest_PUT(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{Name: "test"}
	handler := agent.handleSchemaRequest(cap)

	req := httptest.NewRequest("PUT", "/schema", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405 for PUT, got %d", rec.Code)
	}
}

// TestHandleSchemaRequest_DELETE tests that DELETE is not allowed
func TestHandleSchemaRequest_DELETE(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{Name: "test"}
	handler := agent.handleSchemaRequest(cap)

	req := httptest.NewRequest("DELETE", "/schema", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405 for DELETE, got %d", rec.Code)
	}
}

// TestHandleSchemaRequest_MinimalSchema tests endpoint with no InputSummary
func TestHandleSchemaRequest_MinimalSchema(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "minimal",
		Description: "Minimal capability",
	}

	handler := agent.handleSchemaRequest(cap)

	req := httptest.NewRequest("GET", "/schema", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &schema); err != nil {
		t.Errorf("Failed to parse minimal schema: %v", err)
	}

	// Should have basic structure but no properties
	if schema["$schema"] == nil {
		t.Error("Expected $schema even for minimal capability")
	}

	if schema["properties"] != nil {
		t.Error("Minimal schema should not have properties")
	}
}

// ========== Tool Schema Generation Tests ==========

// TestTool_GenerateJSONSchema tests schema generation for tools
func TestTool_GenerateJSONSchema(t *testing.T) {
	tool := NewTool("test-tool")

	cap := Capability{
		Name:        "tool_capability",
		Description: "Tool-based capability",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "input", Type: "string"},
			},
		},
	}

	schema := tool.generateJSONSchema(cap)

	// Verify schema structure is identical to agent
	if schema["$schema"] != "http://json-schema.org/draft-07/schema#" {
		t.Error("Tool schema should match agent schema format")
	}

	if schema["title"] != "tool_capability" {
		t.Error("Tool schema title should match capability name")
	}

	properties := schema["properties"].(map[string]interface{})
	if len(properties) != 1 {
		t.Errorf("Expected 1 property, got %d", len(properties))
	}
}

// TestTool_HandleSchemaRequest tests HTTP schema endpoint for tools
func TestTool_HandleSchemaRequest(t *testing.T) {
	tool := NewTool("test-tool")

	cap := Capability{
		Name: "tool_schema_test",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "param", Type: "number"},
			},
		},
	}

	handler := tool.handleSchemaRequest(cap)

	req := httptest.NewRequest("GET", "/schema", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Tool schema endpoint should return 200, got %d", rec.Code)
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &schema); err != nil {
		t.Errorf("Tool schema should be valid JSON: %v", err)
	}
}

// ========== Integration Tests ==========

// TestCapabilityWithInputSummary_SchemaEndpointGeneration tests complete Phase 2+3 integration
func TestCapabilityWithInputSummary_SchemaEndpointGeneration(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	agent.RegisterCapability(Capability{
		Name:        "weather",
		Description: "Get weather data",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "location", Type: "string"},
			},
		},
	})

	// Verify capability was registered
	if len(agent.Capabilities) != 1 {
		t.Fatal("Capability should be registered")
	}

	cap := agent.Capabilities[0]

	// Verify endpoint was auto-generated
	expectedEndpoint := "/api/capabilities/weather"
	if cap.Endpoint != expectedEndpoint {
		t.Errorf("Expected endpoint %s, got %s", expectedEndpoint, cap.Endpoint)
	}

	// Verify schema endpoint was auto-generated
	expectedSchemaEndpoint := "/api/capabilities/weather/schema"
	if cap.SchemaEndpoint != expectedSchemaEndpoint {
		t.Errorf("Expected schema endpoint %s, got %s", expectedSchemaEndpoint, cap.SchemaEndpoint)
	}

	// Test that schema endpoint is actually callable
	req := httptest.NewRequest("GET", cap.SchemaEndpoint, nil)
	rec := httptest.NewRecorder()

	agent.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Schema endpoint should be accessible, got status %d", rec.Code)
	}

	// Verify response is valid schema
	var schema map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &schema); err != nil {
		t.Errorf("Schema endpoint should return valid JSON: %v", err)
	}

	// Verify schema contains expected fields
	if schema["title"] != "weather" {
		t.Error("Schema should have capability name as title")
	}

	properties := schema["properties"].(map[string]interface{})
	if properties["location"] == nil {
		t.Error("Schema should include location field")
	}
}

// TestCapabilityWithoutInputSummary_NoSchemaEndpoint tests that schema endpoint is NOT created
func TestCapabilityWithoutInputSummary_NoSchemaEndpoint(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	agent.RegisterCapability(Capability{
		Name:        "simple",
		Description: "Simple capability without schema",
	})

	cap := agent.Capabilities[0]

	// Schema endpoint should NOT be generated
	if cap.SchemaEndpoint != "" {
		t.Errorf("Schema endpoint should not be generated without InputSummary, got %s", cap.SchemaEndpoint)
	}

	// Verify schema endpoint is not registered in mux
	schemaPath := "/api/capabilities/simple/schema"
	req := httptest.NewRequest("GET", schemaPath, nil)
	rec := httptest.NewRecorder()

	agent.mux.ServeHTTP(rec, req)

	// Should return 404 since endpoint doesn't exist
	if rec.Code == http.StatusOK {
		t.Error("Schema endpoint should not exist for capability without InputSummary")
	}
}

// TestTool_SchemaEndpointGeneration tests schema endpoint generation for tools
func TestTool_SchemaEndpointGeneration(t *testing.T) {
	tool := NewTool("weather-tool")

	tool.RegisterCapability(Capability{
		Name:        "forecast",
		Description: "Weather forecast",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "city", Type: "string"},
			},
		},
	})

	cap := tool.Capabilities[0]

	// Verify schema endpoint was auto-generated for tool
	expectedSchemaEndpoint := "/api/capabilities/forecast/schema"
	if cap.SchemaEndpoint != expectedSchemaEndpoint {
		t.Errorf("Tool schema endpoint should be %s, got %s", expectedSchemaEndpoint, cap.SchemaEndpoint)
	}

	// Test schema endpoint is callable
	req := httptest.NewRequest("GET", cap.SchemaEndpoint, nil)
	rec := httptest.NewRecorder()

	tool.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Tool schema endpoint should work, got status %d", rec.Code)
	}
}

// TestSchemaEndpoint_CustomEndpoint tests schema generation with custom endpoint
func TestSchemaEndpoint_CustomEndpoint(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	agent.RegisterCapability(Capability{
		Name:        "custom",
		Description: "Custom endpoint capability",
		Endpoint:    "/custom/path",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "data", Type: "object"},
			},
		},
	})

	cap := agent.Capabilities[0]

	// Schema endpoint should follow custom endpoint path
	expectedSchemaEndpoint := "/custom/path/schema"
	if cap.SchemaEndpoint != expectedSchemaEndpoint {
		t.Errorf("Expected schema endpoint %s, got %s", expectedSchemaEndpoint, cap.SchemaEndpoint)
	}

	// Verify it's accessible
	req := httptest.NewRequest("GET", expectedSchemaEndpoint, nil)
	rec := httptest.NewRecorder()

	agent.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Custom schema endpoint should work, got %d", rec.Code)
	}
}

// TestSchemaGeneration_ComplexSchema tests schema generation with many fields
func TestSchemaGeneration_ComplexSchema(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "complex",
		Description: "Complex capability with many fields",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "id", Type: "number", Description: "Unique identifier"},
				{Name: "name", Type: "string", Description: "Display name"},
				{Name: "active", Type: "boolean", Description: "Status flag"},
			},
			OptionalFields: []FieldHint{
				{Name: "tags", Type: "array", Description: "Tag list"},
				{Name: "metadata", Type: "object", Description: "Extra data"},
				{Name: "score", Type: "number", Example: "95.5"},
			},
		},
	}

	schema := agent.generateJSONSchema(cap)

	properties := schema["properties"].(map[string]interface{})
	if len(properties) != 6 {
		t.Errorf("Expected 6 properties (3 required + 3 optional), got %d", len(properties))
	}

	required := schema["required"].([]string)
	if len(required) != 3 {
		t.Errorf("Expected 3 required fields, got %d", len(required))
	}

	// Verify all types are present
	for fieldName, expectedType := range map[string]string{
		"id":       "number",
		"name":     "string",
		"active":   "boolean",
		"tags":     "array",
		"metadata": "object",
		"score":    "number",
	} {
		prop := properties[fieldName].(map[string]interface{})
		if prop["type"] != expectedType {
			t.Errorf("Field %s: expected type %s, got %v", fieldName, expectedType, prop["type"])
		}
	}
}

// TestSchemaGeneration_JSONMarshaling tests that generated schemas can be marshaled
func TestSchemaGeneration_JSONMarshaling(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name:        "marshal_test",
		Description: "Test JSON marshaling",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "field1", Type: "string", Example: "test"},
			},
		},
	}

	schema := agent.generateJSONSchema(cap)

	// Marshal to JSON
	jsonData, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Failed to marshal schema to JSON: %v", err)
	}

	// Unmarshal back
	var reconstructed map[string]interface{}
	if err := json.Unmarshal(jsonData, &reconstructed); err != nil {
		t.Fatalf("Failed to unmarshal schema from JSON: %v", err)
	}

	// Verify structure is preserved
	if reconstructed["$schema"] != schema["$schema"] {
		t.Error("Schema structure not preserved after marshaling")
	}

	// Verify it's valid JSON Schema format
	if reconstructed["type"] != "object" {
		t.Error("Marshaled schema should maintain object type")
	}
}

// TestSchemaGeneration_FieldOrdering tests that fields maintain consistent ordering
func TestSchemaGeneration_FieldOrdering(t *testing.T) {
	agent := NewBaseAgent("test-agent")

	cap := Capability{
		Name: "ordering_test",
		InputSummary: &SchemaSummary{
			RequiredFields: []FieldHint{
				{Name: "alpha", Type: "string"},
				{Name: "beta", Type: "string"},
				{Name: "gamma", Type: "string"},
			},
		},
	}

	// Generate schema multiple times
	schema1 := agent.generateJSONSchema(cap)
	schema2 := agent.generateJSONSchema(cap)

	// Required fields should maintain insertion order
	required1 := schema1["required"].([]string)
	required2 := schema2["required"].([]string)

	if !reflect.DeepEqual(required1, required2) {
		t.Error("Required field ordering should be consistent across generations")
	}

	// Should maintain order: alpha, beta, gamma
	if len(required1) != 3 || required1[0] != "alpha" || required1[1] != "beta" || required1[2] != "gamma" {
		t.Errorf("Required fields should maintain insertion order, got %v", required1)
	}
}
