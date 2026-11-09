package main

import (
	"net/http"
	"strings"
)

// Data inspection can decode large retained payloads. Keep the number of
// simultaneous reads bounded independently of the number of browser tabs.
// Health checks and application mutations do not use these slots.
const viewerConcurrentDataReads = 4

var viewerDataReadSlots = make(chan struct{}, viewerConcurrentDataReads)

func withViewerDataReadBudget(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		limited := path == "/api/executions" || strings.HasPrefix(path, "/api/executions/") ||
			path == "/api/conversations" || path == "/api/llm-debug" || strings.HasPrefix(path, "/api/llm-debug/")
		if !limited {
			next(w, r)
			return
		}
		// These routes only inspect data. Reject unsupported methods before any
		// handler reads a record, including when the read budget is saturated.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		select {
		case viewerDataReadSlots <- struct{}{}:
			defer func() { <-viewerDataReadSlots }()
			next(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			if !responseNoStore(w.Header()) {
				w.Header().Set("Cache-Control", "no-store")
			}
			http.Error(w, "The viewer is handling other data reads. Try again shortly.", http.StatusTooManyRequests)
		}
	}
}
