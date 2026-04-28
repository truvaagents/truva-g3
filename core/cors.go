// Package core provides the core functionality for the Truva-G3 framework.
package core

import (
	"fmt"
	"net/http"
	"strings"
)

// CORSMiddleware creates a CORS middleware handler for HTTP servers.
// This middleware handles both preflight (OPTIONS) requests and adds
// appropriate CORS headers to responses based on the provided configuration.
//
// The middleware supports:
//   - Wildcard origins ("*" for all origins)
//   - Wildcard subdomains ("*.example.com")
//   - Wildcard ports ("http://localhost:*")
//   - Credential-based requests (cookies, auth headers)
//
// Example usage:
//
//	mux := http.NewServeMux()
//	corsConfig := &CORSConfig{
//	    Enabled: true,
//	    AllowedOrigins: []string{"https://example.com"},
//	    AllowCredentials: true,
//	}
//	handler := CORSMiddleware(corsConfig)(mux)
//	http.ListenAndServe(":8080", handler)
func CORSMiddleware(config *CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip CORS if not enabled
			if !config.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")

			// Check if origin is allowed
			if isOriginAllowed(origin, config.AllowedOrigins) {
				// Set CORS headers
				w.Header().Set("Access-Control-Allow-Origin", origin)

				if config.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}

				// Set allowed methods
				if len(config.AllowedMethods) > 0 {
					w.Header().Set("Access-Control-Allow-Methods", strings.Join(config.AllowedMethods, ", "))
				}

				// Set allowed headers
				if len(config.AllowedHeaders) > 0 {
					w.Header().Set("Access-Control-Allow-Headers", strings.Join(config.AllowedHeaders, ", "))
				}

				// Set exposed headers
				if len(config.ExposedHeaders) > 0 {
					w.Header().Set("Access-Control-Expose-Headers", strings.Join(config.ExposedHeaders, ", "))
				}

				// Set max age for preflight caching
				if config.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", config.MaxAge))
				}
			}

			// Handle preflight OPTIONS request
			if r.Method == http.MethodOptions {
				// Preflight request - just return the headers
				w.WriteHeader(http.StatusNoContent)
				return
			}

			// Continue with the next handler
			next.ServeHTTP(w, r)
		})
	}
}

// isOriginAllowed checks if an origin is allowed based on the configuration.
// This function implements the origin matching logic including:
//   - Exact origin matching
//   - Wildcard all origins ("*")
//   - Wildcard subdomain matching ("*.example.com")
//   - Wildcard port matching ("http://localhost:*")
//
// Returns true if the origin is allowed, false otherwise.
// An empty origin (same-origin request) returns false as CORS headers
// are not needed for same-origin requests.
func isOriginAllowed(origin string, allowedOrigins []string) bool {
	// Empty origin means same-origin request or no origin header
	if origin == "" {
		return false
	}

	for _, allowed := range allowedOrigins {
		// Allow all origins
		if allowed == "*" {
			return true
		}

		// Exact match
		if allowed == origin {
			return true
		}

		// Wildcard subdomain support (e.g., *.example.com or https://*.example.com)
		if strings.Contains(allowed, "*.") {
			// Find the wildcard position
			wildcardIdx := strings.Index(allowed, "*.")

			// Get the parts before and after the wildcard
			beforeWildcard := allowed[:wildcardIdx]
			afterWildcard := allowed[wildcardIdx+2:] // Skip "*."

			// Check if origin starts with the part before wildcard (if any)
			if !strings.HasPrefix(origin, beforeWildcard) {
				continue
			}

			// Check if origin ends with the part after wildcard
			if !strings.HasSuffix(origin, afterWildcard) {
				continue
			}

			// Extract the middle part that replaces the wildcard
			remainingOrigin := origin[len(beforeWildcard):]
			remainingOrigin = strings.TrimSuffix(remainingOrigin, afterWildcard)

			// The wildcard part must:
			// 1. Not be empty (root domain shouldn't match)
			// 2. End with a dot (to ensure it's a complete subdomain)
			if len(remainingOrigin) > 0 && strings.HasSuffix(remainingOrigin+".", ".") {
				return true
			}
		}

		// Wildcard port support (e.g., http://localhost:*)
		if strings.Contains(allowed, ":*") {
			// Get the base URL without port
			baseAllowed := strings.Split(allowed, ":*")[0]
			// Check if origin starts with the base URL
			if strings.HasPrefix(origin, baseAllowed+":") {
				return true
			}
		}
	}

	return false
}

// ApplyCORS applies CORS headers to a ResponseWriter based on the configuration.
// This is a lower-level function that can be used when you need to apply CORS
// headers manually without using the middleware.
//
// This function is useful when:
//   - You have custom middleware ordering requirements
//   - You need conditional CORS application
//   - You're working with WebSocket upgrades or SSE
//
// Example:
//
//	func myHandler(w http.ResponseWriter, r *http.Request) {
//	    ApplyCORS(w, r, corsConfig)
//	    // ... rest of handler
//	}
func ApplyCORS(w http.ResponseWriter, r *http.Request, config *CORSConfig) {
	if !config.Enabled {
		return
	}

	origin := r.Header.Get("Origin")

	if isOriginAllowed(origin, config.AllowedOrigins) {
		w.Header().Set("Access-Control-Allow-Origin", origin)

		if config.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if len(config.AllowedMethods) > 0 {
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(config.AllowedMethods, ", "))
		}

		if len(config.AllowedHeaders) > 0 {
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(config.AllowedHeaders, ", "))
		}

		if len(config.ExposedHeaders) > 0 {
			w.Header().Set("Access-Control-Expose-Headers", strings.Join(config.ExposedHeaders, ", "))
		}
	}
}

// DefaultCORSConfig returns a secure default CORS configuration.
// CORS is disabled by default for security. Enable and configure
// allowed origins explicitly for production use.
//
// Default configuration:
//   - Enabled: false (must explicitly enable)
//   - AllowedOrigins: empty (must specify origins)
//   - AllowedMethods: GET, POST, PUT, DELETE, OPTIONS
//   - AllowedHeaders: Content-Type, Authorization
//   - AllowCredentials: false (cookies/auth not allowed)
//   - MaxAge: 86400 seconds (24 hours)
func DefaultCORSConfig() *CORSConfig {
	return &CORSConfig{
		Enabled:          false, // Disabled by default for security
		AllowedOrigins:   []string{},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		ExposedHeaders:   []string{},
		AllowCredentials: false,
		MaxAge:           86400, // 24 hours
	}
}

// DevelopmentCORSConfig returns a permissive CORS configuration for development.
// This configuration allows all origins, methods, and headers with credentials.
//
// WARNING: This configuration is INSECURE and should NEVER be used in production!
// It completely bypasses CORS security mechanisms.
//
// Use this only for local development when:
//   - Testing with multiple local ports
//   - Rapid prototyping without CORS concerns
//   - Development tools need unrestricted access
//
// For production, always use DefaultCORSConfig() with specific allowed origins.
func DevelopmentCORSConfig() *CORSConfig {
	return &CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"*"},
		AllowCredentials: true,
		MaxAge:           86400,
	}
}
