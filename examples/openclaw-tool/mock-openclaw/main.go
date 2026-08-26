// Command mock-openclaw is a TEST DOUBLE for the real OpenClaw gateway sidecar, which is
// not publicly available. It implements just enough of the OpenResponses HTTP API
// (POST /v1/responses) to exercise the openclaw-tool adapter end-to-end: registration,
// the serialized transaction, workspace reset + input write, response mapping, error
// handling, tracing, and logging.
//
// It does NOT summarize. It proves the plumbing by reading /workspace/input.txt (the shared
// emptyDir the adapter wrote) and echoing a marker plus the forwarded prompt. A real
// OpenClaw image swaps in 1:1 with no adapter changes.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type responsesRequest struct {
	Model   string `json:"model"`
	Input   string `json:"input"`
	Session string `json:"session"`
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		var req responsesRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		// Read the input the adapter wrote to the shared workspace — proves the
		// reset+write+shared-volume machinery works.
		data, _ := os.ReadFile("/workspace/input.txt")
		preview := string(data)
		if len(preview) > 120 {
			preview = preview[:120]
		}

		log.Printf("mock /v1/responses: input_file=%d bytes", len(data))

		out := fmt.Sprintf(
			"MOCK OpenClaw response (test double — no real summarization). "+
				"Read %d chars from /workspace/input.txt (preview: %q). Prompt was: %q",
			len(data), preview, truncate(req.Input, 200))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"output_text": out})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mock-openclaw ok"))
	})

	const addr = ":18789"
	log.Printf("mock-openclaw listening on %s", addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
