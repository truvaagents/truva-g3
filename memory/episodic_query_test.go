package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func checkNewestEventQueries(t *testing.T, local, other core.EpisodicMemory) {
	t.Helper()
	ctx := context.Background()
	// Future event time intentionally differs from Redis ingestion time.
	base := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	event := func(id string, ts time.Time, scope core.MemoryScope) core.AgentEvent {
		return core.AgentEvent{EventID: id, Timestamp: ts, AgentDomain: "query-local", AgentName: "worker",
			Scope: scope, ActionType: "inspect", Entities: []core.EntityRef{{Type: "document", ID: "one"}}}
	}
	// Newest first on insertion: reverse-insertion implementations get this wrong.
	for i, id := range []string{"newest", "second", "third"} {
		require.NoError(t, local.RecordEvent(ctx, event(id, base.Add(-time.Duration(i)*time.Second), core.ScopeSharedDomain)))
	}
	oldGlobal := event("old-global", base.Add(-time.Hour), core.ScopeGlobal)
	oldGlobal.AgentDomain = "query-other"
	require.NoError(t, other.RecordEvent(ctx, oldGlobal))
	// Hidden/post-filtered entries exceed the former limit*2 overfetch and a page.
	for i := 0; i < episodicReadBatchSize+3; i++ {
		e := event(fmt.Sprintf("hidden-%03d", i), base.Add(time.Minute), core.ScopePrivate)
		e.ActionType = "hidden"
		require.NoError(t, local.RecordEvent(ctx, e))
	}
	ids := func(events []core.AgentEvent) []string {
		out := make([]string, len(events))
		for i := range events {
			out[i] = events[i].EventID
		}
		return out
	}
	for _, filter := range []core.EventFilter{
		{Limit: 3},
		{Limit: 3, Since: base.Add(-2 * time.Second)},
		{Limit: 3, EntityType: "document", EntityID: "one", ActionTypes: []string{"inspect"}},
		{Limit: 3, AgentName: "worker", ActionTypes: []string{"inspect"}},
	} {
		got, err := local.QueryEvents(ctx, "query-local", filter)
		require.NoError(t, err)
		require.Equal(t, []string{"newest", "second", "third"}, ids(got))
	}
	got, err := local.QueryRecentEvents(ctx, "query-local", base.Add(-2*time.Second), 3)
	require.NoError(t, err)
	require.Equal(t, []string{"newest", "second", "third"}, ids(got))
	got, err = local.QueryEvents(ctx, "query-local", core.EventFilter{Until: base.Add(-time.Second), Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"second", "third"}, ids(got))
	// A late global event can win. A local global copy is returned once only.
	global := event("new-global", base.Add(10*time.Second), core.ScopeGlobal)
	global.AgentDomain = "query-other"
	require.NoError(t, other.RecordEvent(ctx, global))
	require.NoError(t, local.RecordEvent(ctx, event("local-global", base.Add(9*time.Second), core.ScopeGlobal)))
	got, err = local.QueryEvents(ctx, "query-local", core.EventFilter{Limit: 3})
	require.NoError(t, err)
	require.Equal(t, []string{"new-global", "local-global", "newest"}, ids(got))
	got, err = local.QueryEvents(ctx, "unrelated", core.EventFilter{Limit: 3})
	require.NoError(t, err)
	require.Equal(t, []string{"new-global", "local-global", "old-global"}, ids(got))
	// Exact time bounds and ties within one Redis millisecond bucket.
	for _, id := range []string{"tie-z", "tie-a"} {
		require.NoError(t, local.RecordEvent(ctx, event(id, base.Add(20*time.Second+2*time.Nanosecond), core.ScopeSharedDomain)))
	}
	require.NoError(t, local.RecordEvent(ctx, event("nano-newer", base.Add(20*time.Second+3*time.Nanosecond), core.ScopeSharedDomain)))
	for _, filter := range []core.EventFilter{
		{Limit: 2, Since: base.Add(20 * time.Second), Until: base.Add(20*time.Second + 2*time.Nanosecond)},
		{Limit: 2, AgentName: "worker", Since: base.Add(20 * time.Second), Until: base.Add(20*time.Second + 2*time.Nanosecond)},
	} {
		got, err = local.QueryEvents(ctx, "query-local", filter)
		require.NoError(t, err)
		require.Equal(t, []string{"tie-a", "tie-z"}, ids(got))
	}
	got, err = local.QueryEvents(ctx, "query-local", core.EventFilter{Since: base.Add(20*time.Second + 3*time.Nanosecond), Limit: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"nano-newer"}, ids(got))
}

func TestEpisodicNewestEventQueries(t *testing.T) {
	t.Run("in-memory", func(t *testing.T) {
		mem := NewInMemoryEpisodicMemory("query-local", 1000)
		checkNewestEventQueries(t, mem, mem)
	})
	t.Run("redis", func(t *testing.T) {
		_, client := setupMiniRedis(t)
		local, err := NewStreamEpisodicMemory(WithEpisodicRedisClient(client), WithEpisodicDomain("query-local"))
		require.NoError(t, err)
		other, err := NewStreamEpisodicMemory(WithEpisodicRedisClient(client), WithEpisodicDomain("query-other"))
		require.NoError(t, err)
		checkNewestEventQueries(t, local, other)
	})
}

func TestObservedInMemoryRead(t *testing.T) {
	mem := NewInMemoryEpisodicMemory("local", 10)
	var reader core.ObservedEpisodicReader = mem
	got, err := reader.QueryEventsObserved(t.Context(), "local", core.EventFilter{})
	require.NoError(t, err)
	require.Empty(t, got)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reader.QueryEventsObserved(ctx, "local", core.EventFilter{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestEpisodicQueryReadFailures(t *testing.T) {
	for _, corrupt := range []string{"domain-stream", "global-stream", "index", "hash-type", "hash-json", "missing-id", "cancelled"} {
		t.Run(corrupt, func(t *testing.T) {
			_, client := setupMiniRedis(t)
			logger := &memoryCaptureLogger{}
			mem, err := NewStreamEpisodicMemory(WithEpisodicRedisClient(client), WithEpisodicLogger(logger))
			require.NoError(t, err)
			ctx := core.WithRequestID(t.Context(), "read-failure-probe")
			require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{EventID: "one", AgentName: "worker", Scope: core.ScopeGlobal}))
			filter := core.EventFilter{Limit: 1}
			switch corrupt {
			case "domain-stream":
				require.NoError(t, client.Set(ctx, mem.domainStreamKey(), "wrong type", 0).Err())
			case "global-stream":
				require.NoError(t, client.Set(ctx, mem.globalStreamKey(), "wrong type", 0).Err())
			case "index":
				filter.AgentName = "worker"
				require.NoError(t, client.Set(ctx, mem.agentIndexKey("worker"), "wrong type", 0).Err())
			case "hash-type":
				require.NoError(t, client.Del(ctx, mem.eventKey("one")).Err())
				require.NoError(t, client.LPush(ctx, mem.eventKey("one"), "wrong type").Err())
			case "hash-json":
				require.NoError(t, client.Set(ctx, mem.eventKey("one"), "{", 0).Err())
			case "missing-id":
				require.NoError(t, client.XAdd(ctx, &redis.XAddArgs{Stream: mem.domainStreamKey(), Values: map[string]any{"bad": "entry"}}).Err())
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := mem.QueryEventsObserved(ctx, "default", filter)
			require.Error(t, err)
			require.Empty(t, got, "an incomplete scan must not be treated as the newest page")
			got, err = mem.QueryEvents(ctx, "default", filter)
			require.NoError(t, err, "preserve the documented legacy fail-open API")
			require.Empty(t, got)
			fields := logger.fieldsForOperation("query_events")
			require.NotNil(t, fields)
			require.Equal(t, "memory event read failed", fields["error"])
			require.Equal(t, "episodic_read", fields["error_type"])
			require.Equal(t, "read-failure-probe", fields["request_id"])
			require.Equal(t, "default", fields["caller_domain"])
		})
	}
}

func TestEpisodicExpiredEntriesAndDomainIdentity(t *testing.T) {
	_, client := setupMiniRedis(t)
	ctx := context.Background()
	local, err := NewStreamEpisodicMemory(WithEpisodicRedisClient(client), WithEpisodicDomain("a"))
	require.NoError(t, err)
	other, err := NewStreamEpisodicMemory(WithEpisodicRedisClient(client), WithEpisodicDomain("b"))
	require.NoError(t, err)
	for _, mem := range []*StreamEpisodicMemory{local, other} {
		require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{EventID: "same", AgentDomain: mem.domain, Timestamp: time.Unix(1, 0), Scope: core.ScopeGlobal}))
	}
	got, err := local.QueryEvents(ctx, "a", core.EventFilter{})
	require.NoError(t, err)
	require.Len(t, got, 2, "equal IDs in different domain keys are distinct records")
	require.Equal(t, "a", got[0].AgentDomain)
	require.NoError(t, client.Del(ctx, local.eventKey("same")).Err())
	got, err = local.QueryEvents(ctx, "a", core.EventFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "b", got[0].AgentDomain)
}
