package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// generateOpenAPISpec builds an OpenAPI 3.0.0 document from a component's
// registered capabilities. No external dependencies — uses plain maps to stay
// consistent with the existing generateJSONSchema approach.
//
// All capabilities are included, including those marked Internal. The Internal
// flag means "hidden from LLM planning" — it does not mean "hidden from API
// documentation". Internal capabilities are still callable HTTP endpoints
// (chat streams, session management, admin endpoints) that developers need to
// see in API docs. They are tagged with "internal" so users can distinguish
// them from public capabilities.
func generateOpenAPISpec(name string, componentType ComponentType, capabilities []Capability) map[string]interface{} {
	spec := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":       name,
			"description": fmt.Sprintf("Auto-generated OpenAPI spec for %s %s", componentType, name),
			"version":     "1.0.0",
		},
	}

	paths := map[string]interface{}{}
	schemas := map[string]interface{}{}

	// Sort capabilities by name for deterministic output.
	sorted := make([]Capability, 0, len(capabilities))
	sorted = append(sorted, capabilities...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, cap := range sorted {
		operation := buildOperation(cap, schemas)

		endpoint := cap.Endpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("/api/capabilities/%s", cap.Name)
		}

		// Capabilities are invoked via POST with JSON body.
		paths[endpoint] = map[string]interface{}{
			"post": operation,
		}
	}

	// Always document the standard framework endpoints.
	paths["/api/capabilities"] = map[string]interface{}{
		"get": map[string]interface{}{
			"operationId": "listCapabilities",
			"summary":     "List all registered capabilities",
			"tags":        []string{"framework"},
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "Array of registered capabilities",
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type":  "array",
								"items": map[string]interface{}{"type": "object"},
							},
						},
					},
				},
			},
		},
	}
	paths["/health"] = map[string]interface{}{
		"get": map[string]interface{}{
			"operationId": "healthCheck",
			"summary":     "Health check",
			"tags":        []string{"framework"},
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "Component is healthy",
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"status": map[string]interface{}{"type": "string"},
									"type":   map[string]interface{}{"type": "string"},
									"name":   map[string]interface{}{"type": "string"},
									"id":     map[string]interface{}{"type": "string"},
								},
							},
						},
					},
				},
			},
		},
	}

	spec["paths"] = paths
	if len(schemas) > 0 {
		spec["components"] = map[string]interface{}{
			"schemas": schemas,
		}
	}

	return spec
}

// buildOperation creates an OpenAPI operation object for a single Capability.
// If InputSummary/OutputSummary are present, referenced schemas are added to
// the schemas map (populated as a side effect for $ref resolution).
func buildOperation(cap Capability, schemas map[string]interface{}) map[string]interface{} {
	tag := string(cap.Type)
	if tag == "" {
		tag = "tool"
	}
	tags := []string{tag}
	if cap.Internal {
		tags = append(tags, "internal")
	}

	op := map[string]interface{}{
		"operationId": cap.Name,
		"summary":     cap.Description,
		"tags":        tags,
	}

	// Request body
	if cap.InputSummary != nil {
		schemaName := cap.Name + "Input"
		schemas[schemaName] = schemaSummaryToJSONSchema(cap.InputSummary)
		op["requestBody"] = map[string]interface{}{
			"required": true,
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{
						"$ref": "#/components/schemas/" + schemaName,
					},
				},
			},
		}
	} else {
		// Minimal request body — description-only capability.
		op["requestBody"] = map[string]interface{}{
			"required": true,
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{"type": "object"},
				},
			},
		}
	}

	// Responses
	successResp := map[string]interface{}{
		"description": "Successful response",
	}
	if cap.OutputSummary != nil {
		schemaName := cap.Name + "Output"
		schemas[schemaName] = schemaSummaryToJSONSchema(cap.OutputSummary)
		successResp["content"] = map[string]interface{}{
			"application/json": map[string]interface{}{
				"schema": map[string]interface{}{
					"$ref": "#/components/schemas/" + schemaName,
				},
			},
		}
	}
	op["responses"] = map[string]interface{}{
		"200": successResp,
	}

	return op
}

// schemaSummaryToJSONSchema converts a SchemaSummary into an OpenAPI-compatible
// JSON Schema object. Uses the same FieldHint mapping as the existing
// fieldHintToJSONSchema methods on BaseTool/BaseAgent.
func schemaSummaryToJSONSchema(summary *SchemaSummary) map[string]interface{} {
	schema := map[string]interface{}{
		"type": "object",
	}

	if summary == nil {
		return schema
	}

	properties := map[string]interface{}{}
	var required []string

	for _, field := range summary.RequiredFields {
		properties[field.Name] = fieldHintToOpenAPIProperty(field)
		required = append(required, field.Name)
	}

	for _, field := range summary.OptionalFields {
		properties[field.Name] = fieldHintToOpenAPIProperty(field)
	}

	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

// fieldHintToOpenAPIProperty converts a FieldHint to an OpenAPI schema property.
func fieldHintToOpenAPIProperty(field FieldHint) map[string]interface{} {
	prop := map[string]interface{}{
		"type": field.Type,
	}
	if field.Description != "" {
		prop["description"] = field.Description
	}
	if field.Example != "" {
		prop["example"] = field.Example
	}
	return prop
}

// openAPIHandler returns an http.HandlerFunc that serves the OpenAPI spec.
// The handler reads capabilities at request time so the spec reflects any
// capabilities registered after startup.
func openAPIHandler(name string, componentType ComponentType, getCapabilities func() []Capability) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		spec := generateOpenAPISpec(name, componentType, getCapabilities())

		// Inject the servers field so spec consumers ("Try it out" forms,
		// generated client SDKs, gateway importers) target the same URL the
		// spec was fetched from. Honors the standard X-Forwarded-* reverse
		// proxy headers so that when this component is reached through a
		// path-prefixed proxy, the rendered server URL round-trips through
		// that proxy instead of pointing at the upstream Host the proxy
		// forwarded with. Falls back to r.Host when no proxy headers are set.
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		host := r.Host
		if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
			// May be comma-separated when multiple proxies are in the chain
			// (browser → ingress → service mesh → ...). The leftmost entry is
			// the original host the browser hit, which is what we want for the
			// rendered server URL.
			if i := strings.IndexByte(fwdHost, ','); i >= 0 {
				fwdHost = fwdHost[:i]
			}
			host = strings.TrimSpace(fwdHost)
		}
		prefix := r.Header.Get("X-Forwarded-Prefix")
		spec["servers"] = []map[string]interface{}{
			{"url": fmt.Sprintf("%s://%s%s", scheme, host, prefix), "description": name},
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if err := json.NewEncoder(w).Encode(spec); err != nil {
			http.Error(w, "Failed to encode OpenAPI spec", http.StatusInternalServerError)
		}
	}
}
