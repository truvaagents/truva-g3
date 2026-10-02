package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// These cases protect the distinction between a bounded view and missing data.
func TestLLMEnrichmentReasons(t *testing.T) {
	for _, test := range []struct {
		name, raw, reason        string
		executions, interactions int
	}{
		{"normal", `{}`, "", 1, 1},
		{"parse", `{`, "parse_failure", 1, 1},
		{"bytes", strings.Repeat("x", viewerLLMInteractionBytesLimit+1), "interaction_byte_limit", 1, 1},
		{"interactions", `{}`, "interaction_count_limit", 1, viewerLLMInteractionLimit + 1},
		{"executions", `{}`, "execution_count_limit", viewerLLMEnrichmentExecutionLimit + 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			previousClient, previousKeys, previousMock := viewerRedisClient, viewerLLMKeys, useMock
			viewerRedisClient, useMock = client, false
			keyspace, err := core.NewRedisKeyspace("enrichment-reasons")
			if err != nil {
				t.Fatal(err)
			}
			viewerLLMKeys = telemetry.NewRedisLLMDebugKeys(keyspace)
			t.Cleanup(func() {
				viewerRedisClient, viewerLLMKeys, useMock = previousClient, previousKeys, previousMock
				_ = client.Close()
			})
			var summaries []ExecutionSummary
			for i := 0; i < test.executions; i++ {
				summaries = append(summaries, ExecutionSummary{RequestID: fmt.Sprintf("request-%d", i)})
			}
			for i := 0; i < test.interactions; i++ {
				if err := client.RPush(t.Context(), viewerLLMKeys.Interactions("request-0"), test.raw).Err(); err != nil {
					t.Fatal(err)
				}
			}
			_, incomplete, reasons, err := enrichExecutionSummariesWithDiagnostics(t.Context(), summaries)
			if err != nil {
				t.Fatal(err)
			}
			if test.reason == "" {
				if incomplete || len(reasons) != 0 {
					t.Fatalf("false incomplete: %v", reasons)
				}
				return
			}
			if !incomplete || len(reasons) != 1 || reasons[0].Code != test.reason {
				t.Fatalf("diagnostics: %v, %v", incomplete, reasons)
			}
		})
	}
}
