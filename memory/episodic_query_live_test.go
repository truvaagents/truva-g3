//go:build integration

package memory

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

// Explicit opt-in: isolated Redis namespace, no AI, no application case/PR.
func TestEpisodicNewestFirstRedisLive(t *testing.T) {
	if os.Getenv("TRUVAG3_EPISODIC_QUERY_LIVE") != "1" {
		t.Skip("set TRUVAG3_EPISODIC_QUERY_LIVE=1 for the isolated Redis check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	connection, err := core.ResolveRedisConnectionConfig(nil, os.LookupEnv)
	require.NoError(t, err)
	client, err := core.NewRedisUniversalClient(connection)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	runID := fmt.Sprintf("newest-query-check-%d", time.Now().UnixNano())
	keyspace, err := core.NewRedisKeyspace(runID)
	require.NoError(t, err)
	newMemory := func(domain string) *StreamEpisodicMemory {
		mem, err := NewStreamEpisodicMemory(WithEpisodicRedisClient(client), WithEpisodicKeyspace(keyspace), WithEpisodicDomain(domain), WithEpisodicEventTTL(time.Hour))
		require.NoError(t, err)
		return mem
	}
	local, other := newMemory("query-local"), newMemory("query-other")
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		// Explicit keys owned by this run only, no wildcard deletion/shared history.
		for _, mem := range []*StreamEpisodicMemory{local, other} {
			require.NoError(t, mem.walkEventStream(cleanupCtx, mem.domainStreamKey(), false, func(keys []string) error {
				pipe := client.Pipeline()
				for _, key := range keys {
					pipe.Del(cleanupCtx, key)
				}
				_, err := pipe.Exec(cleanupCtx)
				return err
			}))
			for _, key := range []string{mem.domainStreamKey(), mem.globalStreamKey(), mem.entityIndexKey("document", "one"), mem.agentIndexKey("worker")} {
				require.NoError(t, client.Del(cleanupCtx, key).Err())
			}
		}
	}()
	checkNewestEventQueries(t, local, other)
	if os.Getenv("TRUVAG3_EPISODIC_QUERY_SCALE") == "1" {
		// Representative larger retained history, not an atomicity/load benchmark.
		for _, mem := range []*StreamEpisodicMemory{local, other} {
			for i := 0; i < 10000; i++ {
				require.NoError(t, mem.RecordEvent(ctx, core.AgentEvent{
					EventID: fmt.Sprintf("scale-%05d", i), AgentDomain: mem.domain,
					AgentName: "worker", Timestamp: time.Unix(1000+int64(i), 0), Scope: core.ScopeGlobal,
					Summary: "Labelled synthetic historical event for newest-first query verification",
				}))
			}
		}
		t.Log("scale fixture added 20000 retained events across two domains, all globally visible")
	}
	start := time.Now()
	for i := 0; i < 20; i++ {
		got, err := local.queryEventsNewestFirst(ctx, "query-local", core.EventFilter{Limit: 3})
		require.NoError(t, err)
		require.Len(t, got, 3)
		require.Equal(t, "nano-newer", got[0].EventID)
	}
	t.Logf("run=%s ordering/filter/scope/paging checks passed; 20 queries in %s; paid_calls=0", runID, time.Since(start))
}
