package main

import (
	"context"
	"crypto/sha1" // #nosec G505 — used for non-secret artifact ID derivation only
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ArtifactStore stores raw PR patches/files outside of orchestration state.
// The agent receives only handle IDs in manifests; raw bytes are fetched on
// demand via Get / GetSlice.
type ArtifactStore interface {
	Put(ctx context.Context, bundleID, name string, data []byte) (ArtifactRef, error)
	Get(ctx context.Context, bundleID, artifactID string) ([]byte, error)
	// GetSlice returns the requested byte window plus the artifact's total
	// size, so callers can compute whether more bytes exist past the slice.
	GetSlice(ctx context.Context, bundleID, artifactID string, req SliceRequest) (data []byte, totalSize int64, err error)
	Health(ctx context.Context) string
	Backend() string
}

type SliceRequest struct {
	ByteStart int64 `json:"byte_start"`
	ByteLimit int64 `json:"byte_limit"`
}

// NewArtifactStore returns a backend per cfg.ArtifactBackend. Only "redis" is
// implemented in the MVP; other values return an explicit error so misconfig
// fails loudly at startup.
func NewArtifactStore(cfg Config, redisClient *redis.Client) (ArtifactStore, error) {
	switch cfg.ArtifactBackend {
	case "redis", "":
		if redisClient == nil {
			return nil, fmt.Errorf("redis artifact backend selected but redis client is nil")
		}
		// The store enforces a single defensive ceiling so callers can't
		// accidentally store gigantic blobs. Per-payload-type caps (patch vs
		// file) are enforced at the handler boundary, where the type is known.
		ceiling := cfg.MaxPatchBytes
		if cfg.MaxFileBytes > ceiling {
			ceiling = cfg.MaxFileBytes
		}
		return &RedisArtifactStore{
			Client:           redisClient,
			TTL:              cfg.ArtifactTTL,
			MaxArtifactBytes: ceiling,
			MaxSliceBytes:    cfg.MaxSliceBytes,
		}, nil
	case "filesystem", "s3":
		return nil, fmt.Errorf("artifact backend %q is not implemented in MVP (only 'redis' is supported)", cfg.ArtifactBackend)
	default:
		return nil, fmt.Errorf("unknown artifact backend %q", cfg.ArtifactBackend)
	}
}

// --- Redis implementation ---

type RedisArtifactStore struct {
	Client           *redis.Client
	TTL              time.Duration
	MaxArtifactBytes int64
	MaxSliceBytes    int64
}

func (s *RedisArtifactStore) Backend() string { return "redis" }

func (s *RedisArtifactStore) Health(ctx context.Context) string {
	if err := s.Client.Ping(ctx).Err(); err != nil {
		return "error"
	}
	return "ok"
}

func (s *RedisArtifactStore) Put(ctx context.Context, bundleID, name string, data []byte) (ArtifactRef, error) {
	if s.MaxArtifactBytes > 0 && int64(len(data)) > s.MaxArtifactBytes {
		return ArtifactRef{}, fmt.Errorf("artifact %q too large: %d bytes (limit %d)", name, len(data), s.MaxArtifactBytes)
	}

	id := NewArtifactID(name)
	key := artifactKey(bundleID, id)

	if err := s.Client.Set(ctx, key, data, s.TTL).Err(); err != nil {
		return ArtifactRef{}, fmt.Errorf("redis set: %w", err)
	}
	return ArtifactRef{
		ID:        id,
		Backend:   "redis",
		SizeBytes: int64(len(data)),
		MediaType: "text/plain",
	}, nil
}

func (s *RedisArtifactStore) Get(ctx context.Context, bundleID, artifactID string) ([]byte, error) {
	key := artifactKey(bundleID, artifactID)
	data, err := s.Client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("artifact not found: %s", artifactID)
	}
	if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}
	return data, nil
}

func (s *RedisArtifactStore) GetSlice(ctx context.Context, bundleID, artifactID string, req SliceRequest) ([]byte, int64, error) {
	if req.ByteLimit <= 0 {
		return nil, 0, fmt.Errorf("byte_limit must be positive")
	}
	if s.MaxSliceBytes > 0 && req.ByteLimit > s.MaxSliceBytes {
		return nil, 0, fmt.Errorf("byte_limit %d exceeds maximum %d", req.ByteLimit, s.MaxSliceBytes)
	}
	if req.ByteStart < 0 {
		return nil, 0, fmt.Errorf("byte_start must be non-negative")
	}
	data, err := s.Get(ctx, bundleID, artifactID)
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(data))
	start := req.ByteStart
	if start > total {
		start = total
	}
	end := start + req.ByteLimit
	if end > total {
		end = total
	}
	return data[start:end], total, nil
}

// --- Helpers ---

func artifactKey(bundleID, artifactID string) string {
	return "github-tool:artifact:" + bundleID + ":" + artifactID
}

// NewArtifactID derives a deterministic, filesystem-safe ID from a payload
// name. Same name within a bundle = same ID, so re-storing is idempotent.
func NewArtifactID(name string) string {
	sum := sha1.Sum([]byte(name)) // #nosec G401 — not used for security
	return "art_" + hex.EncodeToString(sum[:8])
}

// NewBundleID composes a stable ID from the PR coordinates and head SHA.
// Same PR + same head SHA = same bundle ID, which lets re-runs reuse artifacts.
func NewBundleID(owner, repo string, pullNumber int, headSHA string) string {
	short := headSHA
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("prb_%s_%s_%d_%s",
		sanitize(owner), sanitize(repo), pullNumber, short)
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", ":", "_", " ", "_", ".", "_")
	return r.Replace(s)
}
