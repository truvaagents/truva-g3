package telemetry

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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

	metricLabelEligibilityProperty = "truvag3_metric_label"
	metricLabelExcludedValue       = "false"
)

// BaggageExactErrorReason is a bounded classification for exact-baggage
// validation and capacity failures. It never contains the rejected key or
// value.
type BaggageExactErrorReason string

const (
	BaggageExactInvalidUTF8  BaggageExactErrorReason = "invalid_utf8"
	BaggageExactInvalidKey   BaggageExactErrorReason = "invalid_baggage_key"
	BaggageExactKeyTooLong   BaggageExactErrorReason = "key_too_long"
	BaggageExactValueTooLong BaggageExactErrorReason = "value_too_long"
	BaggageExactItemLimit    BaggageExactErrorReason = "item_limit"
	BaggageExactTotalSize    BaggageExactErrorReason = "total_size"
)

// BaggageExactError reports why WithBaggageExact rejected a member without
// retaining or exposing the rejected data.
type BaggageExactError struct {
	Reason BaggageExactErrorReason
}

func (e *BaggageExactError) Error() string {
	if e == nil {
		return "exact baggage rejected"
	}
	return "exact baggage rejected: " + string(e.Reason)
}

// BaggageExactErrorReasonOf returns the bounded reason carried by an exact
// baggage error.
func BaggageExactErrorReasonOf(err error) (BaggageExactErrorReason, bool) {
	var exactErr *BaggageExactError
	if !errors.As(err, &exactErr) {
		return "", false
	}
	return exactErr.Reason, true
}

type baggageMemberOptions struct {
	metricLabelEligible *bool
}

// BaggageMemberOption configures generic baggage-member behavior.
type BaggageMemberOption func(*baggageMemberOptions)

// WithMetricLabelEligibility controls whether a baggage member may
// automatically enrich context-aware metric labels. The decision is stored as
// a W3C baggage-member property so it survives standard propagation.
func WithMetricLabelEligibility(eligible bool) BaggageMemberOption {
	return func(options *baggageMemberOptions) {
		options.metricLabelEligible = &eligible
	}
}

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
		updateBaggageTotalSize(bag)
		// Could log warning here, but keeping it silent as per original design
		return ctx // Return unchanged context when at limit
	}

	newBag := bag
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

		// Create baggage member
		member, err := baggage.NewMember(key, value)
		if err != nil {
			// Invalid key/value, skip
			continue
		}

		replacing := newBag.Member(key).Key() != ""
		if !replacing && newBag.Len() >= MaxBaggageItems {
			baggageItemsDropped.Add(1)
			baggageOverLimit.Add(1)
			continue
		}
		candidateBag, err := newBag.SetMember(member)
		if err != nil {
			// Skip members that fail to set
			continue
		}
		if serializedBaggageSize(candidateBag) > MaxBaggageTotalSize {
			baggageItemsDropped.Add(1)
			continue
		}

		newBag = candidateBag
		baggageItemsAdded.Add(1)
	}

	updateBaggageTotalSize(newBag)
	return baggage.ContextWithBaggage(ctx, newBag)
}

// WithBaggageExact adds or replaces one baggage member without truncation.
// It validates the complete result against the framework's baggage limits and
// returns the original context with a typed error on rejection.
func WithBaggageExact(
	ctx context.Context,
	key string,
	rawValue string,
	options ...BaggageMemberOption,
) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	bag := baggage.FromContext(ctx)
	reject := func(reason BaggageExactErrorReason, overLimit bool) (context.Context, error) {
		baggageItemsDropped.Add(1)
		if overLimit {
			baggageOverLimit.Add(1)
		}
		updateBaggageTotalSize(bag)
		return ctx, &BaggageExactError{Reason: reason}
	}

	if !utf8.ValidString(key) || !utf8.ValidString(rawValue) {
		return reject(BaggageExactInvalidUTF8, false)
	}
	if key == "" {
		return reject(BaggageExactInvalidKey, false)
	}
	if len(key) > MaxBaggageKeyLength {
		return reject(BaggageExactKeyTooLong, false)
	}
	if len(rawValue) > MaxBaggageValueLength {
		return reject(BaggageExactValueTooLong, false)
	}
	if _, err := baggage.NewMember(key, ""); err != nil {
		return reject(BaggageExactInvalidKey, false)
	}

	memberOptions := baggageMemberOptions{}
	for _, option := range options {
		if option != nil {
			option(&memberOptions)
		}
	}

	properties := make([]baggage.Property, 0, 1)
	if memberOptions.metricLabelEligible != nil {
		propertyValue := "true"
		if !*memberOptions.metricLabelEligible {
			propertyValue = metricLabelExcludedValue
		}
		property, err := baggage.NewKeyValueProperty(
			metricLabelEligibilityProperty,
			propertyValue,
		)
		if err != nil {
			return reject(BaggageExactInvalidKey, false)
		}
		properties = append(properties, property)
	}

	member, err := baggage.NewMemberRaw(key, rawValue, properties...)
	if err != nil {
		return reject(BaggageExactInvalidKey, false)
	}

	replacing := bag.Member(key).Key() != ""
	if !replacing && bag.Len() >= MaxBaggageItems {
		return reject(BaggageExactItemLimit, true)
	}

	updatedBag, err := bag.SetMember(member)
	if err != nil {
		return reject(BaggageExactInvalidKey, false)
	}
	updatedSize := serializedBaggageSize(updatedBag)
	if updatedSize > MaxBaggageTotalSize {
		return reject(BaggageExactTotalSize, true)
	}

	if !replacing {
		baggageItemsAdded.Add(1)
	}
	updateBaggageTotalSize(updatedBag)
	return baggage.ContextWithBaggage(ctx, updatedBag), nil
}

// WithoutBaggageMember removes one member while preserving every other member
// and its W3C properties.
func WithoutBaggageMember(ctx context.Context, key string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if key == "" {
		return ctx
	}

	bag := baggage.FromContext(ctx).DeleteMember(key)
	updateBaggageTotalSize(bag)
	return baggage.ContextWithBaggage(ctx, bag)
}

// CopyBaggage copies the complete OTel baggage object, including member
// properties, from src to dst. It preserves destination cancellation and
// deadlines and does not change baggage usage counters.
func CopyBaggage(dst, src context.Context) context.Context {
	if dst == nil {
		dst = context.Background()
	}
	if src == nil {
		return dst
	}
	bag := baggage.FromContext(src)
	if bag.Len() == 0 {
		return dst
	}
	return baggage.ContextWithBaggage(dst, bag)
}

func serializedBaggageSize(bag baggage.Baggage) int {
	return len(bag.String())
}

func updateBaggageTotalSize(bag baggage.Baggage) {
	size := serializedBaggageSize(bag)
	if size >= 0 {
		baggageTotalSize.Store(uint64(size))
	}
}

func metricLabelEligible(member baggage.Member) bool {
	for _, property := range member.Properties() {
		if property.Key() != metricLabelEligibilityProperty {
			continue
		}
		value, hasValue := property.Value()
		return !hasValue || value != metricLabelExcludedValue
	}
	return true
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
		if !metricLabelEligible(m) {
			continue
		}
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
