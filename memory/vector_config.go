package memory

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// VectorConfig holds configuration for the vector DB SharedKnowledge backend.
type VectorConfig struct {
	// Connection
	Address string // gRPC address, e.g. "qdrant:6334"
	APIKey  string // Optional API key
	TLS     bool   // Enable TLS for gRPC connection

	// Collection
	CollectionName string // Default: "truvag3_knowledge"
	VectorSize     int    // Embedding dimension. Default: 768 (nomic-embed-text)
	Distance       string // Distance metric. Default: "Cosine"

	// Timeouts
	ConnectTimeout time.Duration // Default: 5s
	SearchTimeout  time.Duration // Default: 3s

	// Auto-create collection if not exists
	AutoCreateCollection bool // Default: true

	// Observability
	Logger    core.Logger
	Telemetry core.Telemetry
}

// Option configures vector DBSharedKnowledge.
type Option func(*VectorConfig) error

// WithVectorAddress sets the vector DB gRPC address.
func WithVectorAddress(addr string) Option {
	return func(c *VectorConfig) error {
		if addr == "" {
			return fmt.Errorf("vector DB address cannot be empty")
		}
		c.Address = addr
		return nil
	}
}

// WithVectorAPIKey sets the vector DB API key for authentication.
func WithVectorAPIKey(key string) Option {
	return func(c *VectorConfig) error {
		c.APIKey = key
		return nil
	}
}

// WithVectorTLS enables TLS for the gRPC connection.
func WithVectorTLS(enabled bool) Option {
	return func(c *VectorConfig) error {
		c.TLS = enabled
		return nil
	}
}

// WithCollectionName sets the vector DB collection name.
func WithCollectionName(name string) Option {
	return func(c *VectorConfig) error {
		if name == "" {
			return fmt.Errorf("collection name cannot be empty")
		}
		c.CollectionName = name
		return nil
	}
}

// WithVectorSize sets the embedding dimension for the collection.
func WithVectorSize(size int) Option {
	return func(c *VectorConfig) error {
		if size <= 0 {
			return fmt.Errorf("vector size must be positive, got %d", size)
		}
		c.VectorSize = size
		return nil
	}
}

// WithDistance sets the distance metric (Cosine, Euclid, Dot).
func WithDistance(distance string) Option {
	return func(c *VectorConfig) error {
		c.Distance = distance
		return nil
	}
}

// WithConnectTimeout sets the gRPC connection timeout.
func WithConnectTimeout(d time.Duration) Option {
	return func(c *VectorConfig) error {
		c.ConnectTimeout = d
		return nil
	}
}

// WithSearchTimeout sets the search operation timeout.
func WithSearchTimeout(d time.Duration) Option {
	return func(c *VectorConfig) error {
		c.SearchTimeout = d
		return nil
	}
}

// WithAutoCreateCollection enables or disables auto-creation of the collection.
func WithAutoCreateCollection(enabled bool) Option {
	return func(c *VectorConfig) error {
		c.AutoCreateCollection = enabled
		return nil
	}
}

// WithLogger sets the logger for vector DB operations.
// Rejects nil — use &core.NoOpLogger{} to explicitly disable logging.
func WithLogger(logger core.Logger) Option {
	return func(c *VectorConfig) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		c.Logger = logger
		return nil
	}
}

// WithTelemetry sets the telemetry provider for vector DB operations.
// Rejects nil — use &core.NoOpTelemetry{} to explicitly disable telemetry.
func WithTelemetry(telemetry core.Telemetry) Option {
	return func(c *VectorConfig) error {
		if telemetry == nil {
			return fmt.Errorf("telemetry cannot be nil: use &core.NoOpTelemetry{} to disable telemetry")
		}
		c.Telemetry = telemetry
		return nil
	}
}

// defaultConfig returns the default VectorConfig with env var overrides.
// Precedence: explicit options > TRUVAG3_* env vars > defaults.
func defaultConfig() *VectorConfig {
	config := &VectorConfig{
		Address:              "localhost:6334",
		CollectionName:       "truvag3_knowledge",
		VectorSize:           768,
		Distance:             "Cosine",
		ConnectTimeout:       5 * time.Second,
		SearchTimeout:        3 * time.Second,
		AutoCreateCollection: true,
		Logger:               &core.NoOpLogger{},
		Telemetry:            &core.NoOpTelemetry{},
	}

	// Override with TRUVAG3_* env vars (lower priority than explicit options)
	if addr := os.Getenv("TRUVAG3_VECTOR_DB_URL"); addr != "" {
		config.Address = addr
	}
	if key := os.Getenv("TRUVAG3_VECTOR_DB_API_KEY"); key != "" {
		config.APIKey = key
	}
	if name := os.Getenv("TRUVAG3_VECTOR_DB_COLLECTION"); name != "" {
		config.CollectionName = name
	}
	if sizeStr := os.Getenv("TRUVAG3_VECTOR_DB_VECTOR_SIZE"); sizeStr != "" {
		if size, err := strconv.Atoi(sizeStr); err == nil && size > 0 {
			config.VectorSize = size
		}
	}

	return config
}
