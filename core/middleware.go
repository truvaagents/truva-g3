package core

import (
	"net/http"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher to support SSE streaming.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// LoggingMiddleware logs HTTP requests and responses with structured logging.
// In development mode (devMode=true), it logs all requests.
// In production mode (devMode=false), it only logs non-2xx responses and slow requests (>1s).
func LoggingMiddleware(logger Logger, devMode bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code
			wrapped := &responseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
				written:        false,
			}

			// Call next handler
			next.ServeHTTP(wrapped, r)

			// Calculate duration
			duration := time.Since(start)

			// Determine if we should log this request
			shouldLog := devMode || // Always log in dev mode
				wrapped.statusCode >= 400 || // Log errors
				duration > time.Second // Log slow requests

			if shouldLog && logger != nil {
				logData := map[string]interface{}{
					"method":      r.Method,
					"path":        r.URL.Path,
					"status":      wrapped.statusCode,
					"duration_ms": duration.Milliseconds(),
					"remote_addr": r.RemoteAddr,
					"user_agent":  r.UserAgent(),
				}

				// Add query params if present
				if r.URL.RawQuery != "" {
					logData["query"] = r.URL.RawQuery
				}

				// Add content length if present
				if r.ContentLength > 0 {
					logData["content_length"] = r.ContentLength
				}

				// Log at appropriate level
				if wrapped.statusCode >= 500 {
					logger.ErrorWithContext(r.Context(), "HTTP request error", logData)
				} else if wrapped.statusCode >= 400 {
					logger.WarnWithContext(r.Context(), "HTTP request client error", logData)
				} else if duration > time.Second {
					logger.WarnWithContext(r.Context(), "HTTP request slow", logData)
				} else {
					logger.InfoWithContext(r.Context(), "HTTP request", logData)
				}
			}
		})
	}
}
