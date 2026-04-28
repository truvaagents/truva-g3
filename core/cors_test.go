package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCORSMiddleware verifies CORS middleware functionality
func TestCORSMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		config         *CORSConfig
		requestOrigin  string
		requestMethod  string
		expectedStatus int
		checkHeaders   func(*testing.T, http.Header)
	}{
		{
			name: "CORS disabled",
			config: &CORSConfig{
				Enabled: false,
			},
			requestOrigin:  "https://example.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				// No CORS headers should be set
				assert.Empty(t, headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "Exact origin match",
			config: &CORSConfig{
				Enabled:          true,
				AllowedOrigins:   []string{"https://example.com"},
				AllowedMethods:   []string{"GET", "POST"},
				AllowedHeaders:   []string{"Content-Type"},
				AllowCredentials: true,
			},
			requestOrigin:  "https://example.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				assert.Equal(t, "https://example.com", headers.Get("Access-Control-Allow-Origin"))
				assert.Equal(t, "true", headers.Get("Access-Control-Allow-Credentials"))
				assert.Equal(t, "GET, POST", headers.Get("Access-Control-Allow-Methods"))
				assert.Equal(t, "Content-Type", headers.Get("Access-Control-Allow-Headers"))
			},
		},
		{
			name: "Wildcard all origins",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
			},
			requestOrigin:  "https://any-site.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				assert.Equal(t, "https://any-site.com", headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "Wildcard subdomain match",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://*.example.com"},
			},
			requestOrigin:  "https://api.example.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				assert.Equal(t, "https://api.example.com", headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "Wildcard subdomain no match on root",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://*.example.com"},
			},
			requestOrigin:  "https://example.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				// Should not match root domain
				assert.Empty(t, headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "Wildcard port match",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"http://localhost:*"},
			},
			requestOrigin:  "http://localhost:3000",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				assert.Equal(t, "http://localhost:3000", headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "OPTIONS preflight request",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://example.com"},
				AllowedMethods: []string{"GET", "POST", "PUT"},
				AllowedHeaders: []string{"Content-Type", "Authorization"},
				MaxAge:         86400,
			},
			requestOrigin:  "https://example.com",
			requestMethod:  "OPTIONS",
			expectedStatus: http.StatusNoContent,
			checkHeaders: func(t *testing.T, headers http.Header) {
				assert.Equal(t, "https://example.com", headers.Get("Access-Control-Allow-Origin"))
				assert.Equal(t, "GET, POST, PUT", headers.Get("Access-Control-Allow-Methods"))
				assert.Equal(t, "Content-Type, Authorization", headers.Get("Access-Control-Allow-Headers"))
				assert.Equal(t, "86400", headers.Get("Access-Control-Max-Age"))
			},
		},
		{
			name: "Origin not allowed",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://example.com"},
			},
			requestOrigin:  "https://evil.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				// No CORS headers for disallowed origin
				assert.Empty(t, headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "No origin header",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://example.com"},
			},
			requestOrigin:  "",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				// No CORS headers for same-origin request
				assert.Empty(t, headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "Multiple allowed origins",
			config: &CORSConfig{
				Enabled: true,
				AllowedOrigins: []string{
					"https://app.example.com",
					"https://api.example.com",
					"http://localhost:3000",
				},
			},
			requestOrigin:  "https://api.example.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				assert.Equal(t, "https://api.example.com", headers.Get("Access-Control-Allow-Origin"))
			},
		},
		{
			name: "Exposed headers",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"*"},
				ExposedHeaders: []string{"X-Total-Count", "X-Page"},
			},
			requestOrigin:  "https://example.com",
			requestMethod:  "GET",
			expectedStatus: http.StatusOK,
			checkHeaders: func(t *testing.T, headers http.Header) {
				assert.Equal(t, "X-Total-Count, X-Page", headers.Get("Access-Control-Expose-Headers"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a simple handler
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("OK"))
			})

			// Wrap with CORS middleware
			corsHandler := CORSMiddleware(tt.config)(handler)

			// Create test request
			req := httptest.NewRequest(tt.requestMethod, "/test", nil)
			if tt.requestOrigin != "" {
				req.Header.Set("Origin", tt.requestOrigin)
			}

			// Record response
			recorder := httptest.NewRecorder()
			corsHandler.ServeHTTP(recorder, req)

			// Check status
			assert.Equal(t, tt.expectedStatus, recorder.Code)

			// Check headers
			if tt.checkHeaders != nil {
				tt.checkHeaders(t, recorder.Header())
			}
		})
	}
}

// TestIsOriginAllowed verifies origin matching logic
func TestIsOriginAllowed(t *testing.T) {
	tests := []struct {
		name           string
		origin         string
		allowedOrigins []string
		expected       bool
	}{
		{
			name:           "exact match",
			origin:         "https://example.com",
			allowedOrigins: []string{"https://example.com"},
			expected:       true,
		},
		{
			name:           "no match",
			origin:         "https://evil.com",
			allowedOrigins: []string{"https://example.com"},
			expected:       false,
		},
		{
			name:           "wildcard all",
			origin:         "https://any-site.com",
			allowedOrigins: []string{"*"},
			expected:       true,
		},
		{
			name:           "wildcard subdomain match",
			origin:         "https://api.example.com",
			allowedOrigins: []string{"https://*.example.com"},
			expected:       true,
		},
		{
			name:           "wildcard subdomain deep match",
			origin:         "https://v2.api.example.com",
			allowedOrigins: []string{"https://*.example.com"},
			expected:       true,
		},
		{
			name:           "wildcard subdomain no match on root",
			origin:         "https://example.com",
			allowedOrigins: []string{"https://*.example.com"},
			expected:       false,
		},
		{
			name:           "wildcard subdomain wrong domain",
			origin:         "https://api.evil.com",
			allowedOrigins: []string{"https://*.example.com"},
			expected:       false,
		},
		{
			name:           "wildcard port match",
			origin:         "http://localhost:3000",
			allowedOrigins: []string{"http://localhost:*"},
			expected:       true,
		},
		{
			name:           "wildcard port different port",
			origin:         "http://localhost:8080",
			allowedOrigins: []string{"http://localhost:*"},
			expected:       true,
		},
		{
			name:           "wildcard port wrong host",
			origin:         "http://example.com:3000",
			allowedOrigins: []string{"http://localhost:*"},
			expected:       false,
		},
		{
			name:           "empty origin",
			origin:         "",
			allowedOrigins: []string{"*"},
			expected:       false,
		},
		{
			name:           "multiple allowed origins first match",
			origin:         "https://app.example.com",
			allowedOrigins: []string{"https://app.example.com", "https://api.example.com"},
			expected:       true,
		},
		{
			name:           "multiple allowed origins second match",
			origin:         "https://api.example.com",
			allowedOrigins: []string{"https://app.example.com", "https://api.example.com"},
			expected:       true,
		},
		{
			name:           "case sensitive",
			origin:         "https://Example.com",
			allowedOrigins: []string{"https://example.com"},
			expected:       false,
		},
		{
			name:           "protocol mismatch",
			origin:         "http://example.com",
			allowedOrigins: []string{"https://example.com"},
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isOriginAllowed(tt.origin, tt.allowedOrigins)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestApplyCORS verifies the ApplyCORS function
func TestApplyCORS(t *testing.T) {
	tests := []struct {
		name          string
		config        *CORSConfig
		origin        string
		expectHeaders bool
	}{
		{
			name: "CORS disabled",
			config: &CORSConfig{
				Enabled: false,
			},
			origin:        "https://example.com",
			expectHeaders: false,
		},
		{
			name: "CORS enabled with match",
			config: &CORSConfig{
				Enabled:          true,
				AllowedOrigins:   []string{"https://example.com"},
				AllowedMethods:   []string{"GET", "POST"},
				AllowedHeaders:   []string{"Content-Type"},
				AllowCredentials: true,
			},
			origin:        "https://example.com",
			expectHeaders: true,
		},
		{
			name: "CORS enabled no match",
			config: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{"https://example.com"},
			},
			origin:        "https://evil.com",
			expectHeaders: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			ApplyCORS(recorder, req, tt.config)

			if tt.expectHeaders {
				assert.NotEmpty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
				if tt.config.AllowCredentials {
					assert.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))
				}
			} else {
				assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

// TestDefaultCORSConfig verifies default CORS configuration
func TestDefaultCORSConfig(t *testing.T) {
	config := DefaultCORSConfig()

	assert.False(t, config.Enabled)
	assert.Empty(t, config.AllowedOrigins)
	assert.Equal(t, []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}, config.AllowedMethods)
	assert.Equal(t, []string{"Content-Type", "Authorization"}, config.AllowedHeaders)
	assert.False(t, config.AllowCredentials)
	assert.Equal(t, 86400, config.MaxAge)
}

// TestDevelopmentCORSConfig verifies development CORS configuration
func TestDevelopmentCORSConfig(t *testing.T) {
	config := DevelopmentCORSConfig()

	assert.True(t, config.Enabled)
	assert.Equal(t, []string{"*"}, config.AllowedOrigins)
	assert.Equal(t, []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"}, config.AllowedMethods)
	assert.Equal(t, []string{"*"}, config.AllowedHeaders)
	assert.Equal(t, []string{"*"}, config.ExposedHeaders)
	assert.True(t, config.AllowCredentials)
	assert.Equal(t, 86400, config.MaxAge)
}

// TestCORSIntegration tests CORS with a real HTTP server
func TestCORSIntegration(t *testing.T) {
	// Create a simple API handler
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"success"}`))
	})

	// Configure CORS
	corsConfig := &CORSConfig{
		Enabled: true,
		AllowedOrigins: []string{
			"https://app.example.com",
			"https://*.example.com",
			"http://localhost:3000",
		},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Total-Count", "X-Page-Count"},
		AllowCredentials: true,
		MaxAge:           3600,
	}

	// Create server with CORS middleware
	handler := CORSMiddleware(corsConfig)(apiHandler)
	server := httptest.NewServer(handler)
	defer server.Close()

	t.Run("Preflight request", func(t *testing.T) {
		req, err := http.NewRequest("OPTIONS", server.URL, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Content-Type, Authorization")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		assert.Equal(t, "https://app.example.com", resp.Header.Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "true", resp.Header.Get("Access-Control-Allow-Credentials"))
		assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
		assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Content-Type")
		assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Authorization")
		assert.Equal(t, "3600", resp.Header.Get("Access-Control-Max-Age"))
	})

	t.Run("Actual request with allowed origin", func(t *testing.T) {
		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://api.example.com")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "https://api.example.com", resp.Header.Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "true", resp.Header.Get("Access-Control-Allow-Credentials"))
		assert.Equal(t, "X-Total-Count, X-Page-Count", resp.Header.Get("Access-Control-Expose-Headers"))
	})

	t.Run("Request with disallowed origin", func(t *testing.T) {
		req, err := http.NewRequest("GET", server.URL, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://evil.com")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		// No CORS headers for disallowed origin
		assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	})
}

// BenchmarkCORSMiddleware benchmarks CORS middleware performance
func BenchmarkCORSMiddleware(b *testing.B) {
	config := &CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com", "https://*.example.com"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	corsHandler := CORSMiddleware(config)(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "https://api.example.com")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder := httptest.NewRecorder()
		corsHandler.ServeHTTP(recorder, req)
	}
}

// BenchmarkIsOriginAllowed benchmarks origin matching performance
func BenchmarkIsOriginAllowed(b *testing.B) {
	allowedOrigins := []string{
		"https://app.example.com",
		"https://api.example.com",
		"https://*.example.com",
		"http://localhost:*",
		"https://other.com",
	}

	origin := "https://subdomain.example.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isOriginAllowed(origin, allowedOrigins)
	}
}

// ExampleCORSMiddleware demonstrates using CORS middleware
func ExampleCORSMiddleware() {
	// Create your API handler
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("API response"))
	})

	// Configure CORS
	corsConfig := &CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowCredentials: true,
	}

	// Wrap with CORS middleware
	handler := CORSMiddleware(corsConfig)(apiHandler)

	// Use the handler
	_ = http.ListenAndServe(":8080", handler)
}

// ExampleApplyCORS demonstrates manual CORS application
func ExampleApplyCORS() {
	http.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		// Manually apply CORS
		corsConfig := &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"*"},
		}
		ApplyCORS(w, r, corsConfig)

		// Handle request
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Data"))
	})

	_ = http.ListenAndServe(":8080", nil)
}
