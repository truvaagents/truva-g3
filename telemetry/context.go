package telemetry

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/baggage"
)

// Baggage holds request-scoped telemetry labels that flow through context
type Baggage map[string]string

// Constants for baggage limits to prevent unbounded growth.
// These limits are based on W3C baggage specification recommendations
// and practical experience with distributed systems.
// Exceeding these limits can cause:
//   - Memory exhaustion in high-traffic systems
//   - Network overhead when propagating context
//   - Performance degradation in serialization/deserialization
const (
	// MaxBaggageItems is the maximum number of key-value pairs allowed
	MaxBaggageItems = 64

	// MaxBaggageKeyLength is the maximum bytes for a single key
	MaxBaggageKeyLength = 128

	// MaxBaggageValueLength is the maximum bytes for a single value
	MaxBaggageValueLength = 512

	// MaxBaggageTotalSize is the maximum total size (8KB) for all baggage
	MaxBaggageTotalSize = 8192
)

// Metrics for baggage usage (internal telemetry).
// These help identify when limits are being hit in production.
var (
	baggageItemsAdded   atomic.Uint64 // Successfully added to baggage
	baggageItemsDropped atomic.Uint64 // Dropped due to limits
	baggageOverLimit    atomic.Uint64 // Contexts that hit the item limit
	baggageTotalSize    atomic.Uint64 // Current total size of baggage
)

// labelPool reuses label slices to reduce GC pressure.
// Most metrics have 8-16 labels, so we pre-allocate 16.
// This pool significantly reduces allocations in high-throughput scenarios.
var labelPool = sync.Pool{
	New: func() any {
		// Pre-allocate a reasonable size
		s := make([]string, 0, 32)
		return &s
	},
}

// WithBaggage adds labels that automatically flow through all telemetry in this context.
// Uses OpenTelemetry baggage for standard compliance.
// Labels are key-value pairs passed as variadic strings.
// Example: ctx = telemetry.WithBaggage(ctx, "request_id", reqID, "user_id", userID)
//
// Multiple calls to WithBaggage are additive:
//
//	ctx = telemetry.WithBaggage(ctx, "request_id", "123")
//	ctx = telemetry.WithBaggage(ctx, "user_id", "456")  // Both labels preserved
//
// Later values override earlier ones with the same key:
//
//	ctx = telemetry.WithBaggage(ctx, "env", "staging")
//	ctx = telemetry.WithBaggage(ctx, "env", "production")  // env is now "production"
//
// Limits are enforced:
// - Maximum items: 64
// - Maximum key length: 128 characters
// - Maximum value length: 512 characters
// - Maximum total size: 8KB
func WithBaggage(ctx context.Context, labels ...string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	// Get existing baggage
	bag := baggage.FromContext(ctx)
	members := bag.Members()

	// Check current size
	currentSize := len(members)
	if currentSize >= MaxBaggageItems {
		baggageOverLimit.Add(1)
		// Could log warning here, but keeping it silent as per original design
		return ctx // Return unchanged context when at limit
	}

	// Track total size
	totalSize := 0
	for _, m := range members {
		totalSize += len(m.Key()) + len(m.Value())
	}

	// Process new labels
	var newMembers []baggage.Member
	for i := 0; i < len(labels)-1; i += 2 {
		key := labels[i]
		value := labels[i+1]

		// Validation
		if key == "" {
			continue // Skip empty keys
		}

		// Enforce length limits
		if len(key) > MaxBaggageKeyLength {
			key = key[:MaxBaggageKeyLength]
		}
		if len(value) > MaxBaggageValueLength {
			value = value[:MaxBaggageValueLength]
		}

		// Check total size
		newItemSize := len(key) + len(value)
		if totalSize+newItemSize > MaxBaggageTotalSize {
			baggageItemsDropped.Add(1)
			continue // Skip items that would exceed total size
		}

		// Create baggage member
		member, err := baggage.NewMember(key, value)
		if err != nil {
			// Invalid key/value, skip
			continue
		}

		newMembers = append(newMembers, member)
		totalSize += newItemSize
		baggageItemsAdded.Add(1)
	}

	// Create new baggage with all members
	newBag := bag
	for _, member := range newMembers {
		var err error
		newBag, err = newBag.SetMember(member)
		if err != nil {
			// Skip members that fail to set
			continue
		}
	}

	// Safe conversion: totalSize is bounded by MaxBaggageTotalSize (8192)
	if totalSize >= 0 {
		baggageTotalSize.Store(uint64(totalSize))
	}
	return baggage.ContextWithBaggage(ctx, newBag)
}

// GetBaggage retrieves the current baggage from context as a map.
// Returns nil if no baggage is set.
func GetBaggage(ctx context.Context) Baggage {
	if ctx == nil {
		return nil
	}

	bag := baggage.FromContext(ctx)
	members := bag.Members()
	if len(members) == 0 {
		return nil
	}

	result := make(Baggage, len(members))
	for _, m := range members {
		result[m.Key()] = m.Value()
	}

	return result
}

// appendBaggageToLabels efficiently appends baggage to label slice
// with deterministic ordering (sorted keys) and deduplication
func appendBaggageToLabels(ctx context.Context, labels []string) []string {
	if ctx == nil {
		return labels
	}

	bag := baggage.FromContext(ctx)
	members := bag.Members()
	if len(members) == 0 {
		return labels
	}

	// Get a slice from the pool
	resultPtr := labelPool.Get().(*[]string)
	result := *resultPtr
	result = result[:0] // Reset length but keep capacity

	// Create a map for deduplication (baggage takes precedence)
	labelMap := make(map[string]string, len(labels)/2+len(members))

	// Add explicit labels first
	for i := 0; i < len(labels)-1; i += 2 {
		labelMap[labels[i]] = labels[i+1]
	}

	// Add baggage (overrides explicit labels with same key)
	for _, m := range members {
		labelMap[m.Key()] = m.Value()
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(labelMap))
	for k := range labelMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build result with sorted keys
	for _, k := range keys {
		result = append(result, k, labelMap[k])
	}

	return result
}

// returnLabelSlice returns a label slice to the pool for reuse
func returnLabelSlice(labels []string) {
	if cap(labels) <= 512 { // Don't pool huge slices
		labels = labels[:0] // Reset length to avoid keeping references
		labelPool.Put(&labels)
	}
}

// GetBaggageStats returns internal metrics about baggage usage
type BaggageStats struct {
	ItemsAdded   uint64 `json:"items_added"`
	ItemsDropped uint64 `json:"items_dropped"`
	OverLimit    uint64 `json:"over_limit"`
	CurrentSize  uint64 `json:"current_size"`
}

// GetBaggageStats returns statistics about baggage usage
func GetBaggageStats() BaggageStats {
	return BaggageStats{
		ItemsAdded:   baggageItemsAdded.Load(),
		ItemsDropped: baggageItemsDropped.Load(),
		OverLimit:    baggageOverLimit.Load(),
		CurrentSize:  baggageTotalSize.Load(),
	}
}

// ResetBaggageStats resets baggage statistics (useful for testing)
func ResetBaggageStats() {
	baggageItemsAdded.Store(0)
	baggageItemsDropped.Store(0)
	baggageOverLimit.Store(0)
	baggageTotalSize.Store(0)
}
