package main

import (
	"net/http"
	"strings"
)

// handleAutoResumeSSE is the UI's timeout-approval streaming route.
func (t *HITLChatAgent) handleAutoResumeSSE(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(extractPathParam(r.URL.Path, "/hitl/auto-resume/"), "/stream")
	t.serveResumeSSE(w, r, id, true)
}
