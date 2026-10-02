package memory

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Transport batching only; this never limits the number of candidates examined.
const episodicReadBatchSize = 256

// QueryEventsObserved preserves read failures for callers which need to
// distinguish unavailable memory from an empty retained window.
func (v *StreamEpisodicMemory) QueryEventsObserved(ctx context.Context, callerDomain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	return v.queryEventsNewestFirst(ctx, callerDomain, filter)
}

// QueryEventsObserved uses the same lock-protected view as QueryEvents.
func (m *InMemoryEpisodicMemory) QueryEventsObserved(ctx context.Context, callerDomain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.QueryEvents(ctx, callerDomain, filter)
}

func eventNewer(a, b core.AgentEvent) bool {
	if !a.Timestamp.Equal(b.Timestamp) {
		return a.Timestamp.After(b.Timestamp)
	}
	if a.AgentDomain != b.AgentDomain {
		return a.AgentDomain < b.AgentDomain
	}
	return a.EventID < b.EventID
}

func sortEventsNewestFirst(events []core.AgentEvent) {
	sort.SliceStable(events, func(i, j int) bool { return eventNewer(events[i], events[j]) })
}

// A min-heap keeps only the requested newest events, not all hydrated payloads.
type newestEvents []core.AgentEvent

func (h newestEvents) Len() int           { return len(h) }
func (h newestEvents) Less(i, j int) bool { return eventNewer(h[j], h[i]) }
func (h newestEvents) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *newestEvents) Push(value any)    { *h = append(*h, value.(core.AgentEvent)) }
func (h *newestEvents) Pop() any {
	old := *h
	value := old[len(old)-1]
	old[len(old)-1] = core.AgentEvent{}
	*h = old[:len(old)-1]
	return value
}

func (h *newestEvents) offer(event core.AgentEvent, limit int) {
	if len(*h) < limit {
		heap.Push(h, event)
	} else if eventNewer(event, (*h)[0]) {
		(*h)[0] = event
		heap.Fix(h, 0)
	}
}

func matchesEventFilter(event core.AgentEvent, filter core.EventFilter) bool {
	if (!filter.Since.IsZero() && event.Timestamp.Before(filter.Since)) ||
		(!filter.Until.IsZero() && event.Timestamp.After(filter.Until)) ||
		(filter.AgentName != "" && event.AgentName != filter.AgentName) ||
		(filter.AgentDomain != "" && event.AgentDomain != filter.AgentDomain) ||
		(len(filter.ActionTypes) > 0 && !containsString(filter.ActionTypes, event.ActionType)) {
		return false
	}
	if filter.EntityType == "" && filter.EntityID == "" {
		return true
	}
	for _, entity := range event.Entities {
		if (filter.EntityType == "" || entity.Type == filter.EntityType) &&
			(filter.EntityID == "" || entity.ID == filter.EntityID) {
			return true
		}
	}
	return (filter.EntityType == "" || event.EntityType == filter.EntityType) &&
		(filter.EntityID == "" || event.EntityID == filter.EntityID)
}

func (v *StreamEpisodicMemory) queryEventsNewestFirst(ctx context.Context, callerDomain string, filter core.EventFilter) ([]core.AgentEvent, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	selected := &newestEvents{}
	seen := make(map[string]struct{})
	consume := func(keys []string) error {
		unique := make([]string, 0, len(keys))
		for _, key := range keys {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				unique = append(unique, key)
			}
		}
		return v.readEventBatch(ctx, unique, func(event core.AgentEvent) {
			if v.isVisible(&event, callerDomain, filter.AgentName) && matchesEventFilter(event, filter) {
				selected.offer(event, limit)
			}
		})
	}
	var err error
	switch {
	case filter.EntityType != "" && filter.EntityID != "":
		err = v.walkEventIndex(ctx, v.entityIndexKey(filter.EntityType, filter.EntityID), filter, consume)
	case filter.AgentName != "":
		err = v.walkEventIndex(ctx, v.agentIndexKey(filter.AgentName), filter, consume)
	default:
		// Stream IDs order ingestion, not event time. Scan retained references,
		// including old writers' entries, and filter the authoritative timestamp.
		err = v.walkEventStream(ctx, v.domainStreamKey(), false, consume)
		if err == nil && filter.EntityType == "" && filter.EntityID == "" {
			err = v.walkEventStream(ctx, v.globalStreamKey(), true, consume)
		}
	}
	if err != nil {
		return nil, err // Never present a partly scanned source as a newest page.
	}
	results := []core.AgentEvent(*selected)
	sortEventsNewestFirst(results)
	return results, nil
}

func (v *StreamEpisodicMemory) walkEventStream(ctx context.Context, key string, global bool, consume func([]string) error) error {
	end := "+"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		messages, err := v.client.XRevRangeN(ctx, key, end, "-", episodicReadBatchSize).Result()
		if err != nil {
			return fmt.Errorf("read episodic stream: %w", err)
		}
		keys := make([]string, 0, len(messages))
		for _, message := range messages {
			id, ok := message.Values["event_id"].(string)
			if !ok || id == "" {
				return fmt.Errorf("episodic stream entry missing event identity")
			}
			domain := v.domain
			if global {
				if origin, ok := message.Values["domain"].(string); ok && origin != "" {
					domain = origin
				}
			}
			keys = append(keys, v.keyspace.Plain("memory", domain, "event", id))
		}
		if err := consume(keys); err != nil {
			return err
		}
		if len(messages) < episodicReadBatchSize {
			return nil
		}
		// Exclusive cursor fixes the upper ingestion boundary, even if writers
		// keep appending. It does not promise an atomic cross-stream snapshot.
		end = "(" + messages[len(messages)-1].ID
	}
}

func (v *StreamEpisodicMemory) walkEventIndex(ctx context.Context, key string, filter core.EventFilter, consume func([]string) error) error {
	min, max := "-inf", "+inf"
	if !filter.Since.IsZero() {
		min = strconv.FormatInt(filter.Since.UnixMilli(), 10)
	}
	if !filter.Until.IsZero() {
		max = strconv.FormatInt(filter.Until.UnixMilli(), 10)
	}
	for offset := int64(0); ; offset += episodicReadBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Retain compatibility with servers lacking ZRANGE REV.
		//nolint:staticcheck // ZRevRangeByScore remains supported by go-redis/v9.
		ids, err := v.client.ZRevRangeByScore(ctx, key, &redis.ZRangeBy{
			Min: min, Max: max, Offset: offset, Count: episodicReadBatchSize,
		}).Result()
		if err != nil {
			return fmt.Errorf("read episodic index: %w", err)
		}
		keys := make([]string, 0, len(ids))
		for _, id := range ids {
			keys = append(keys, v.eventKey(id))
		}
		if err := consume(keys); err != nil {
			return err
		}
		if len(ids) < episodicReadBatchSize {
			return nil
		}
	}
}

func (v *StreamEpisodicMemory) readEventBatch(ctx context.Context, keys []string, consume func(core.AgentEvent)) error {
	if len(keys) == 0 {
		return nil
	}
	pipe := v.client.Pipeline()
	commands := make([]*redis.StringCmd, len(keys))
	for i, key := range keys {
		commands[i] = pipe.Get(ctx, key)
	}
	// Individual GETs (not MGET) also work across Redis Cluster slots.
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return fmt.Errorf("hydrate episodic batch: %w", err)
	}
	for _, command := range commands {
		data, err := command.Bytes()
		if err == redis.Nil {
			continue // Expected expired/deleted hash; stream references outlive it.
		}
		if err != nil {
			return fmt.Errorf("hydrate episodic event: %w", err)
		}
		var event core.AgentEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode episodic event: %w", err)
		}
		consume(event)
	}
	return nil
}
