package orchestration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type checkpointDiagnosticFailureClient struct {
	redis.UniversalClient
	cause error
}

func (c *checkpointDiagnosticFailureClient) Get(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("", c.cause)
}
func (c *checkpointDiagnosticFailureClient) Publish(context.Context, string, interface{}) *redis.IntCmd {
	return redis.NewIntResult(0, c.cause)
}
func (c *checkpointDiagnosticFailureClient) TxPipelined(context.Context, func(redis.Pipeliner) error) ([]redis.Cmder, error) {
	return nil, c.cause
}

type checkpointDiagnosticLogger struct {
	core.NoOpLogger
	contexts []context.Context
	fields   []map[string]interface{}
}

func (l *checkpointDiagnosticLogger) ErrorWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	l.contexts = append(l.contexts, ctx)
	l.fields = append(l.fields, fields)
}

func TestCheckpointErrorsPreserveCallerSpanAndBoundDiagnostics(t *testing.T) {
	for _, operation := range []string{"save", "load", "delete", "publish"} {
		t.Run(operation, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			cause := errors.New("redis://user:diagnostic-secret@private-host/0")
			store, err := NewRedisCheckpointStoreWithClient(&checkpointDiagnosticFailureClient{client, cause})
			require.NoError(t, err)
			logger := &checkpointDiagnosticLogger{}
			store.logger = logger
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			ctx, span := provider.Tracer("checkpoint-test").Start(core.WithRequestID(t.Context(), "request-test"), "caller")
			switch operation {
			case "publish":
				commands, constructErr := NewRedisCommandStoreWithClient(&checkpointDiagnosticFailureClient{client, cause}, WithCommandStoreLogger(logger))
				require.NoError(t, constructErr)
				err = commands.PublishCommand(ctx, &Command{CheckpointID: "checkpoint-test", Type: CommandApprove})
			case "save":
				err = store.SaveCheckpoint(ctx, &ExecutionCheckpoint{CheckpointID: "checkpoint-test", RequestID: "request-test", Status: CheckpointStatusPending, CreatedAt: time.Now()})
			case "load":
				_, err = store.LoadCheckpoint(ctx, "checkpoint-test")
			case "delete":
				err = store.DeleteCheckpoint(ctx, "checkpoint-test")
			}
			require.ErrorIs(t, err, cause)
			span.End()
			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			require.Equal(t, codes.Unset, spans[0].Status.Code, "adapter must not mark the caller span failed")
			require.Empty(t, spans[0].Events, "adapter must not attach raw Redis exceptions")
			require.NotEmpty(t, logger.fields)
			for index, fields := range logger.fields {
				require.Same(t, ctx, logger.contexts[index])
				require.Equal(t, "request-test", fields["request_id"])
				require.NotEmpty(t, fields["operation"])
				require.NotEmpty(t, fields["error_type"])
				require.NotContains(t, fmt.Sprint(fields), "diagnostic-secret")
				require.NotContains(t, fmt.Sprint(fields), "private-host")
			}
			// The enclosing operation can still record its bounded terminal outcome.
			ctx, caller := provider.Tracer("checkpoint-test").Start(t.Context(), "terminal-caller")
			telemetry.RecordSpanError(ctx, errors.New("checkpoint operation failed"))
			caller.End()
			require.Equal(t, codes.Error, exporter.GetSpans()[1].Status.Code)
		})
	}
}
