package main

import (
	"strings"
	"testing"
)

func TestBuildLogsResponseRedactsCredentials(t *testing.T) {
	t.Parallel()

	const secret = "live-provider-key"
	response := buildLogsResponse([]lokiStream{{
		Stream: map[string]string{"service_name": "travel-chat-agent"},
		Values: [][]string{{
			"1786370582049313766",
			`stdout F {"level":"ERROR","error":"provider rejected api_key=` + secret + `"}`,
		}},
	}}, `{service_name="travel-chat-agent"}`)

	if len(response.Streams) != 1 || len(response.Streams[0].Entries) != 1 {
		t.Fatalf("unexpected response shape: %#v", response)
	}
	line := response.Streams[0].Entries[0].Line
	if strings.Contains(line, secret) {
		t.Fatal("log response retained the credential")
	}
	if !strings.Contains(line, "api_key=[REDACTED]") {
		t.Fatalf("log response did not retain a useful redaction marker: %q", line)
	}
}
