package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestConfiguredDeadlineReachesTransport(t *testing.T) {
	t.Setenv("TRUVAG3_AI_TIMEOUT", "12m")
	for _, explicit := range []time.Duration{0, 9 * time.Minute} {
		for _, parentLimit := range []time.Duration{0, time.Minute} {
			for _, stream := range []bool{false, true} {
				want := 12 * time.Minute
				if explicit > 0 {
					want = explicit
				}
				if parentLimit > 0 {
					want = parentLimit
				}
				calls := 0
				transport := gpt6Transport(func(req *http.Request) (*http.Response, error) {
					calls++
					deadline, ok := req.Context().Deadline()
					require.True(t, ok)
					require.InDelta(t, float64(want), float64(time.Until(deadline)), float64(time.Second))
					response := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
					if stream {
						response = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				})
				client, err := ai.NewRequestClient(ai.WithProvider("openai"), ai.WithAPIKey("synthetic"), ai.WithTimeout(explicit), ai.WithMaxRetries(0), ai.WithHTTPClient(&http.Client{Transport: transport}))
				require.NoError(t, err)
				ctx := t.Context()
				cancel := func() {}
				if parentLimit > 0 {
					ctx, cancel = context.WithTimeout(ctx, parentLimit)
				}
				request := core.NewAIRequest("short response", "deadline-contract")
				if stream {
					_, err = core.StreamAI(ctx, client, request, func(core.StreamChunk) error { return nil })
				} else {
					_, err = client.Generate(ctx, request)
				}
				cancel()
				require.NoError(t, err)
				require.Equal(t, 1, calls)
			}
		}
	}
}
