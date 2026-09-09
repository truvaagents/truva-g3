package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// TestStore indexes test run metadata in the versioned shared DB-0 keyspace.
type TestStore struct {
	rdb      redis.UniversalClient
	keyspace core.RedisKeyspace
}

// RunMetadata is the metadata stored in Redis for each test run
type RunMetadata struct {
	RunID      string      `json:"run_id"`
	TargetURL  string      `json:"target_url"`
	Site       string      `json:"site"`
	ScriptName string      `json:"script_name"`
	Timestamp  string      `json:"timestamp"`
	Summary    TestSummary `json:"summary"`
	Status     string      `json:"status"` // passed, failed, mixed
	S3Prefix   string      `json:"s3_prefix"`
}

// RunFilter defines query filters for listing runs
type RunFilter struct {
	Site     string
	Status   string
	FromDate string
	ToDate   string
	Limit    int
}

// NewTestStore creates a new test store using the shared topology factory.
func NewTestStore(connection core.RedisConnectionConfig, keyspace core.RedisKeyspace) (*TestStore, error) {
	rdb, err := core.NewRedisUniversalClient(connection)
	if err != nil {
		return nil, err
	}
	return &TestStore{rdb: rdb, keyspace: keyspace}, nil
}

func (s *TestStore) runKey(runID string) string {
	return s.keyspace.Plain("playwright", "runs", "record", runID)
}

func (s *TestStore) runSiteIndex(site string) string {
	return s.keyspace.Plain("playwright", "runs", "index", "site", site)
}

func (s *TestStore) runRecentIndex() string {
	return s.keyspace.Plain("playwright", "runs", "index", "recent")
}

func (s *TestStore) scriptKey(site, name string) string {
	return s.keyspace.Plain("playwright", "scripts", "record", site, name)
}

func (s *TestStore) scriptSiteIndex(site string) string {
	return s.keyspace.Plain("playwright", "scripts", "index", "site", site)
}

// IndexRun stores test run metadata in Redis
func (s *TestStore) IndexRun(ctx context.Context, meta RunMetadata) error {
	// Store run metadata as JSON
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal run metadata: %w", err)
	}

	key := s.runKey(meta.RunID)
	if err := s.rdb.Set(ctx, key, data, 30*24*time.Hour).Err(); err != nil { // 30-day TTL
		return fmt.Errorf("failed to store run metadata: %w", err)
	}

	// Add to site-sorted set (scored by timestamp for ordering)
	ts, _ := time.Parse(time.RFC3339, meta.Timestamp)
	score := float64(ts.Unix())

	siteKey := s.runSiteIndex(meta.Site)
	if err := s.rdb.ZAdd(ctx, siteKey, redis.Z{
		Score:  score,
		Member: meta.RunID,
	}).Err(); err != nil {
		return fmt.Errorf("failed to index run by site: %w", err)
	}
	if err := s.rdb.ZAdd(ctx, s.runRecentIndex(), redis.Z{Score: score, Member: meta.RunID}).Err(); err != nil {
		return fmt.Errorf("failed to index recent run: %w", err)
	}

	return nil
}

// QueryRuns retrieves test runs matching the given filter
func (s *TestStore) QueryRuns(ctx context.Context, filter RunFilter) ([]RunMetadata, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}

	var runIDs []string

	if filter.Site != "" {
		// Query by site using sorted set
		siteKey := s.runSiteIndex(filter.Site)

		// Build score range from date filters
		minScore := "-inf"
		maxScore := "+inf"

		if filter.FromDate != "" {
			if t, err := time.Parse("2006-01-02", filter.FromDate); err == nil {
				minScore = strconv.FormatInt(t.Unix(), 10)
			}
		}
		if filter.ToDate != "" {
			if t, err := time.Parse("2006-01-02", filter.ToDate); err == nil {
				// End of day
				maxScore = strconv.FormatInt(t.Add(24*time.Hour-time.Second).Unix(), 10)
			}
		}

		// Keep the legacy command for Redis-compatible providers without ZRANGE REV.
		//nolint:staticcheck // ZRevRangeByScore remains supported by go-redis/v9.
		ids, err := s.rdb.ZRevRangeByScore(ctx, siteKey, &redis.ZRangeBy{
			Min:   minScore,
			Max:   maxScore,
			Count: int64(limit),
		}).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to query runs by site: %w", err)
		}
		runIDs = ids
	} else {
		ids, err := s.rdb.ZRevRange(ctx, s.runRecentIndex(), 0, int64(limit-1)).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to query recent runs: %w", err)
		}
		runIDs = ids
	}

	// Fetch metadata for each run ID
	var results []RunMetadata
	for _, runID := range runIDs {
		key := s.runKey(runID)
		data, err := s.rdb.Get(ctx, key).Bytes()
		if err != nil {
			continue // Skip missing/expired entries
		}

		var meta RunMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		// Apply status filter
		if filter.Status != "" && meta.Status != filter.Status {
			continue
		}

		results = append(results, meta)
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// ScriptMetadata is the extended metadata stored for each script in Redis
type ScriptMetadata struct {
	Name          string   `json:"name"`
	S3Path        string   `json:"s3_path"`
	Version       int      `json:"version"`
	TestNames     []string `json:"test_names"`
	TestCount     int      `json:"test_count"`
	LastRunStatus string   `json:"last_run_status"` // passed, failed, mixed, ""
	LastRunDate   string   `json:"last_run_date"`   // YYYY-MM-DD or ""
	Updated       string   `json:"updated"`
}

// SaveScriptRef stores a reference to a script's S3 location with test metadata
func (s *TestStore) SaveScriptRef(ctx context.Context, site string, meta ScriptMetadata) error {
	meta.Updated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal script metadata: %w", err)
	}

	key := s.scriptKey(site, meta.Name)
	if err := s.rdb.Set(ctx, key, data, 0).Err(); err != nil { // No TTL for scripts
		return fmt.Errorf("failed to save script ref: %w", err)
	}

	// Add to site's script set
	siteKey := s.scriptSiteIndex(site)
	s.rdb.SAdd(ctx, siteKey, meta.Name)

	return nil
}

// GetScriptRef retrieves full script metadata for a named script
func (s *TestStore) GetScriptRef(ctx context.Context, site, name string) (*ScriptMetadata, error) {
	key := s.scriptKey(site, name)
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, fmt.Errorf("script not found: %s/%s", site, name)
	}

	var meta ScriptMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse script metadata: %w", err)
	}

	return &meta, nil
}

// ListScripts returns all script metadata for a hostname
func (s *TestStore) ListScripts(ctx context.Context, hostname string) ([]ScriptMetadata, error) {
	siteKey := s.scriptSiteIndex(hostname)
	names, err := s.rdb.SMembers(ctx, siteKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list scripts for %s: %w", hostname, err)
	}

	var scripts []ScriptMetadata
	for _, name := range names {
		meta, err := s.GetScriptRef(ctx, hostname, name)
		if err != nil {
			continue // Skip missing/corrupt entries
		}
		scripts = append(scripts, *meta)
	}

	return scripts, nil
}

// GetRunMetadata retrieves run metadata by run ID
func (s *TestStore) GetRunMetadata(ctx context.Context, runID string) (*RunMetadata, error) {
	key := s.runKey(runID)
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, fmt.Errorf("run not found: %s", runID)
	}

	var meta RunMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse run metadata: %w", err)
	}

	return &meta, nil
}

// UpdateScriptRunStatus updates the last run status and date for a script
func (s *TestStore) UpdateScriptRunStatus(ctx context.Context, site, name, status, date string) error {
	meta, err := s.GetScriptRef(ctx, site, name)
	if err != nil {
		return err
	}

	meta.LastRunStatus = status
	meta.LastRunDate = date
	return s.SaveScriptRef(ctx, site, *meta)
}

// Close closes the Redis connection
func (s *TestStore) Close() error {
	return s.rdb.Close()
}
