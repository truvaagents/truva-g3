package core

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// IncomingCallMiddleware captures the existing X-TruvaG3 request, step and
// original-request headers as one IncomingCall envelope. It clears inherited
// incoming-call facts on every request. Invalid optional correlation omits the
// link and emits a bounded warning; it does not reject application work or
// change current execution identity. It neither authenticates callers nor
// wraps response writers, so streaming interfaces remain available.
func IncomingCallMiddleware(logger Logger) func(http.Handler) http.Handler {
	logger = coreComponentLogger(logger)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithoutIncomingCall(r.Context())
			call, present, err := incomingCallFromHeaders(r.Header)
			if err == nil && present {
				ctx, err = WithIncomingCall(ctx, call)
			}
			if err != nil && logger != nil {
				field, reason := "envelope", "invalid"
				var validationErr *incomingCallValidationError
				if errors.As(err, &validationErr) {
					field, reason = validationErr.field, validationErr.reason
				}
				logger.WarnWithContext(ctx, "Incoming call correlation omitted", map[string]interface{}{
					"operation":  "incoming_call_context",
					"request_id": GetRequestID(ctx),
					"status":     "omitted",
					"error_type": "invalid_correlation",
					"error":      "invalid incoming call correlation envelope",
					"field":      field,
					"reason":     reason,
				})
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func incomingCallFromHeaders(header http.Header) (IncomingCall, bool, error) {
	var call IncomingCall
	present := false
	for _, identifier := range []struct {
		header string
		field  string
		value  *string
	}{
		{"X-TruvaG3-Request-ID", "request_id", &call.RequestID},
		{"X-TruvaG3-Step-ID", "step_id", &call.StepID},
		{"X-TruvaG3-Original-Request-ID", "original_request_id", &call.OriginalRequestID},
	} {
		seen := false
		for name, values := range header {
			if !strings.EqualFold(name, identifier.header) {
				continue
			}
			present = true
			if len(values) == 0 {
				return IncomingCall{}, true, &incomingCallValidationError{field: identifier.field, reason: "empty"}
			}
			for _, value := range values {
				if reason := validateIncomingCallID(value); reason != "" {
					return IncomingCall{}, true, &incomingCallValidationError{field: identifier.field, reason: reason}
				}
				if seen && *identifier.value != value {
					return IncomingCall{}, true, &incomingCallValidationError{field: identifier.field, reason: "conflicting_values"}
				}
				*identifier.value = value
				seen = true
			}
		}
	}
	if present && call.RequestID == "" {
		return IncomingCall{}, true, &incomingCallValidationError{field: "request_id", reason: "missing"}
	}
	return call, present, nil
}

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
