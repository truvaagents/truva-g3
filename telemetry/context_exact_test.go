package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
)

type baggageContextTestKey struct{}

func TestBaggageExactError_ErrorIsBounded(t *testing.T) {
	var nilError *BaggageExactError
	if got := nilError.Error(); got != "exact baggage rejected" {
		t.Fatalf("nil error message = %q", got)
	}

	err := &BaggageExactError{Reason: BaggageExactTotalSize}
	if got := err.Error(); got != "exact baggage rejected: total_size" {
		t.Fatalf("typed error message = %q", got)
	}
}

func TestWithBaggageExact_RoundTripsAndExcludesMetricLabel(t *testing.T) {
	values := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"tenant:550e8400-e29b-41d4-a716-446655440000",
		"conversation%2Fopaque",
		strings.Repeat("x", MaxBaggageValueLength),
	}

	for _, value := range values {
		t.Run(fmt.Sprintf("length_%d", len(value)), func(t *testing.T) {
			ctx, err := WithBaggageExact(
				context.Background(),
				"conversation_id",
				value,
				WithMetricLabelEligibility(false),
			)
			if err != nil {
				t.Fatalf("WithBaggageExact() error = %v", err)
			}

			carrier := propagation.MapCarrier{}
			propagator := propagation.Baggage{}
			propagator.Inject(ctx, carrier)
			extracted := propagator.Extract(context.Background(), carrier)

			if got := GetBaggage(extracted)["conversation_id"]; got != value {
				t.Fatalf("round-trip value = %q, want %q", got, value)
			}
			if labelsContainKey(appendBaggageToLabels(extracted, nil), "conversation_id") {
				t.Fatal("metric-ineligible member was copied into metric labels")
			}
		})
	}
}

func TestWithBaggageExact_UnmarkedMemberRemainsMetricEligible(t *testing.T) {
	ctx, err := WithBaggageExact(context.Background(), "custom_id", "value")
	if err != nil {
		t.Fatalf("WithBaggageExact() error = %v", err)
	}
	if !labelsContainKey(appendBaggageToLabels(ctx, nil), "custom_id") {
		t.Fatal("unmarked member should retain existing metric enrichment behavior")
	}
}

func TestWithBaggageExact_ValidationFailureIsTypedAndUnchanged(t *testing.T) {
	base := WithBaggage(context.Background(), "preserved", "value")
	ResetBaggageStats()

	got, err := WithBaggageExact(base, "conversation_id", string([]byte{0xff}))
	if err == nil {
		t.Fatal("expected invalid UTF-8 error")
	}
	if got != base {
		t.Fatal("rejected exact baggage should return the original context")
	}
	reason, ok := BaggageExactErrorReasonOf(err)
	if !ok || reason != BaggageExactInvalidUTF8 {
		t.Fatalf("error reason = %q, %v; want %q, true", reason, ok, BaggageExactInvalidUTF8)
	}
	if !errors.As(err, new(*BaggageExactError)) {
		t.Fatalf("error type = %T, want *BaggageExactError", err)
	}
	stats := GetBaggageStats()
	if stats.ItemsDropped != 1 || stats.OverLimit != 0 {
		t.Fatalf("stats = %+v, want one validation drop and no capacity hit", stats)
	}
	if gotBag := GetBaggage(got); gotBag["preserved"] != "value" {
		t.Fatalf("existing baggage changed: %v", gotBag)
	}
}

func TestWithBaggageExact_RejectsInvalidW3CKey(t *testing.T) {
	for _, key := range []string{"", "invalid key"} {
		t.Run(fmt.Sprintf("key_%q", key), func(t *testing.T) {
			ctx, err := WithBaggageExact(context.Background(), key, "value")
			assertExactReason(t, err, BaggageExactInvalidKey)
			if len(GetBaggage(ctx)) != 0 {
				t.Fatalf("invalid key changed baggage: %v", GetBaggage(ctx))
			}
		})
	}
}

func TestWithBaggageExact_LengthRejectionsAndSuccessfulAddStats(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  BaggageExactErrorReason
	}{
		{
			name:  "key too long",
			key:   strings.Repeat("k", MaxBaggageKeyLength+1),
			value: "value",
			want:  BaggageExactKeyTooLong,
		},
		{
			name:  "value too long",
			key:   "key",
			value: strings.Repeat("v", MaxBaggageValueLength+1),
			want:  BaggageExactValueTooLong,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ResetBaggageStats()
			ctx, err := WithBaggageExact(context.Background(), test.key, test.value)
			assertExactReason(t, err, test.want)
			if len(GetBaggage(ctx)) != 0 {
				t.Fatalf("rejected member changed baggage: %v", GetBaggage(ctx))
			}
			if stats := GetBaggageStats(); stats.ItemsDropped != 1 {
				t.Fatalf("rejection stats = %+v", stats)
			}
		})
	}

	ResetBaggageStats()
	ctx, err := WithBaggageExact(nil, "key", "value", WithMetricLabelEligibility(true))
	if err != nil {
		t.Fatalf("WithBaggageExact(nil) error = %v", err)
	}
	if GetBaggage(ctx)["key"] != "value" {
		t.Fatalf("nil-context add failed: %v", GetBaggage(ctx))
	}
	stats := GetBaggageStats()
	if stats.ItemsAdded != 1 || stats.ItemsDropped != 0 || stats.CurrentSize == 0 {
		t.Fatalf("successful-add stats = %+v", stats)
	}
}

func TestWithBaggageExact_ItemLimitAllowsReplacement(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < MaxBaggageItems; i++ {
		var err error
		ctx, err = WithBaggageExact(ctx, fmt.Sprintf("key%d", i), "value")
		if err != nil {
			t.Fatalf("fill member %d: %v", i, err)
		}
	}

	ResetBaggageStats()
	replaced, err := WithBaggageExact(ctx, "key0", "replacement")
	if err != nil {
		t.Fatalf("replacement at item limit failed: %v", err)
	}
	if got := GetBaggage(replaced)["key0"]; got != "replacement" {
		t.Fatalf("replacement value = %q", got)
	}
	stats := GetBaggageStats()
	if stats.ItemsAdded != 0 || stats.ItemsDropped != 0 || stats.OverLimit != 0 {
		t.Fatalf("replacement stats = %+v", stats)
	}

	ResetBaggageStats()
	unchanged, err := WithBaggageExact(ctx, "new_key", "value")
	assertExactReason(t, err, BaggageExactItemLimit)
	if unchanged != ctx {
		t.Fatal("item-limit rejection should return the original context")
	}
	stats = GetBaggageStats()
	if stats.ItemsDropped != 1 || stats.OverLimit != 1 {
		t.Fatalf("item-limit stats = %+v", stats)
	}
}

func TestWithBaggageExact_PropertyCountsTowardTotalSize(t *testing.T) {
	members := make([]baggage.Member, 0, 16)
	for i := 0; i < 15; i++ {
		member, err := baggage.NewMemberRaw(
			fmt.Sprintf("k%d", i),
			strings.Repeat("x", MaxBaggageValueLength),
		)
		if err != nil {
			t.Fatalf("NewMemberRaw() error = %v", err)
		}
		members = append(members, member)
	}
	oldMember, err := baggage.NewMemberRaw("conversation_id", "old")
	if err != nil {
		t.Fatalf("NewMemberRaw() error = %v", err)
	}
	members = append(members, oldMember)
	bag, err := baggage.New(members...)
	if err != nil {
		t.Fatalf("baggage.New() error = %v", err)
	}
	if size := serializedBaggageSize(bag); size > MaxBaggageTotalSize {
		t.Fatalf("test setup baggage size = %d, exceeds cap", size)
	}
	ctx := baggage.ContextWithBaggage(context.Background(), bag)

	ResetBaggageStats()
	unchanged, err := WithBaggageExact(
		ctx,
		"conversation_id",
		strings.Repeat("y", MaxBaggageValueLength),
		WithMetricLabelEligibility(false),
	)
	assertExactReason(t, err, BaggageExactTotalSize)
	if unchanged != ctx {
		t.Fatal("total-size rejection should return the original context")
	}
	if got := GetBaggage(unchanged)["conversation_id"]; got != "old" {
		t.Fatalf("old member = %q, want old", got)
	}
	stats := GetBaggageStats()
	if stats.ItemsAdded != 0 || stats.ItemsDropped != 1 || stats.OverLimit != 1 {
		t.Fatalf("total-size stats = %+v", stats)
	}
}

func TestWithoutBaggageMember_PreservesPropertiesAndUpdatesSize(t *testing.T) {
	ctx, err := WithBaggageExact(
		context.Background(),
		"preserved",
		"value",
		WithMetricLabelEligibility(false),
	)
	if err != nil {
		t.Fatalf("WithBaggageExact() error = %v", err)
	}
	ctx = WithBaggage(ctx, "conversation_id", "remove-me")

	ResetBaggageStats()
	got := WithoutBaggageMember(ctx, "conversation_id")
	if _, present := GetBaggage(got)["conversation_id"]; present {
		t.Fatal("conversation_id was not removed")
	}
	member := baggage.FromContext(got).Member("preserved")
	if member.Value() != "value" || metricLabelEligible(member) {
		t.Fatalf("preserved member lost value or property: %q, properties=%v", member.Value(), member.Properties())
	}
	stats := GetBaggageStats()
	if stats.ItemsAdded != 0 || stats.ItemsDropped != 0 || stats.OverLimit != 0 {
		t.Fatalf("removal changed counters: %+v", stats)
	}
	if want := uint64(serializedBaggageSize(baggage.FromContext(got))); stats.CurrentSize != want {
		t.Fatalf("CurrentSize = %d, want %d", stats.CurrentSize, want)
	}
}

func TestWithBaggage_CurrentSizeIncludesExistingMemberProperties(t *testing.T) {
	ctx, err := WithBaggageExact(
		context.Background(),
		"conversation_id",
		"conversation-1",
		WithMetricLabelEligibility(false),
	)
	if err != nil {
		t.Fatalf("WithBaggageExact() error = %v", err)
	}
	ResetBaggageStats()
	ctx = WithBaggage(ctx, "agent_name", "travel-chat-agent")

	stats := GetBaggageStats()
	want := uint64(serializedBaggageSize(baggage.FromContext(ctx)))
	if stats.CurrentSize != want {
		t.Fatalf("CurrentSize = %d, want property-aware size %d", stats.CurrentSize, want)
	}
}

func TestWithBaggage_BatchStopsAtItemLimit(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < MaxBaggageItems-1; i++ {
		var err error
		ctx, err = WithBaggageExact(ctx, fmt.Sprintf("key%d", i), "value")
		if err != nil {
			t.Fatalf("fill member %d: %v", i, err)
		}
	}

	ResetBaggageStats()
	got := WithBaggage(
		ctx,
		"last_allowed", "value",
		"over_limit", "value",
	)
	bag := GetBaggage(got)
	if len(bag) != MaxBaggageItems {
		t.Fatalf("baggage item count = %d, want %d", len(bag), MaxBaggageItems)
	}
	if bag["last_allowed"] != "value" {
		t.Fatal("last allowed batch member was not added")
	}
	if _, present := bag["over_limit"]; present {
		t.Fatal("batch member above the item limit was added")
	}
	stats := GetBaggageStats()
	if stats.ItemsAdded != 1 || stats.ItemsDropped != 1 || stats.OverLimit != 1 {
		t.Fatalf("batch item-limit stats = %+v", stats)
	}
}

func TestWithBaggage_UsesSerializedTotalSize(t *testing.T) {
	members := make([]baggage.Member, 0, 15)
	for i := 0; i < 15; i++ {
		member, err := baggage.NewMemberRaw(
			fmt.Sprintf("key%d", i),
			strings.Repeat("x", MaxBaggageValueLength),
		)
		if err != nil {
			t.Fatalf("NewMemberRaw() error = %v", err)
		}
		members = append(members, member)
	}
	bag, err := baggage.New(members...)
	if err != nil {
		t.Fatalf("baggage.New() error = %v", err)
	}
	baseSize := serializedBaggageSize(bag)
	if baseSize > MaxBaggageTotalSize {
		t.Fatalf("test setup baggage size = %d, exceeds cap", baseSize)
	}

	overflow, err := baggage.NewMember(
		"overflow",
		strings.Repeat("y", MaxBaggageValueLength),
	)
	if err != nil {
		t.Fatalf("NewMember() error = %v", err)
	}
	candidate, err := bag.SetMember(overflow)
	if err != nil {
		t.Fatalf("SetMember() error = %v", err)
	}
	if size := serializedBaggageSize(candidate); size <= MaxBaggageTotalSize {
		t.Fatalf("test setup candidate size = %d, want above cap", size)
	}

	ctx := baggage.ContextWithBaggage(context.Background(), bag)
	ResetBaggageStats()
	got := WithBaggage(
		ctx,
		"overflow",
		strings.Repeat("y", MaxBaggageValueLength),
	)
	gotBag := baggage.FromContext(got)
	if gotBag.Member("overflow").Key() != "" {
		t.Fatal("serialized-size overflow member was added")
	}
	if gotBag.Len() != bag.Len() {
		t.Fatalf("baggage item count = %d, want %d", gotBag.Len(), bag.Len())
	}
	for _, member := range members {
		if gotBag.Member(member.Key()).Value() != member.Value() {
			t.Fatalf("existing member %q changed", member.Key())
		}
	}
	stats := GetBaggageStats()
	if stats.ItemsAdded != 0 || stats.ItemsDropped != 1 || stats.OverLimit != 0 {
		t.Fatalf("serialized-size stats = %+v", stats)
	}
	if stats.CurrentSize != uint64(baseSize) {
		t.Fatalf("CurrentSize = %d, want %d", stats.CurrentSize, baseSize)
	}
}

func TestMetricLabelEligibility_IgnoresUnrelatedProperties(t *testing.T) {
	property, err := baggage.NewKeyValueProperty("vendor", "value")
	if err != nil {
		t.Fatalf("NewKeyValueProperty() error = %v", err)
	}
	member, err := baggage.NewMemberRaw("custom_id", "custom-value", property)
	if err != nil {
		t.Fatalf("NewMemberRaw() error = %v", err)
	}
	bag, err := baggage.New(member)
	if err != nil {
		t.Fatalf("baggage.New() error = %v", err)
	}
	ctx := baggage.ContextWithBaggage(context.Background(), bag)

	if !labelsContainKey(appendBaggageToLabels(ctx, nil), "custom_id") {
		t.Fatal("unrelated baggage property made member metric-ineligible")
	}
}

func TestCopyBaggage_PreservesPropertiesDeadlineAndCounters(t *testing.T) {
	src, err := WithBaggageExact(
		context.Background(),
		"conversation_id",
		"conversation-1",
		WithMetricLabelEligibility(false),
	)
	if err != nil {
		t.Fatalf("WithBaggageExact() error = %v", err)
	}
	deadline := time.Now().Add(time.Minute)
	dst, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	ResetBaggageStats()
	got := CopyBaggage(dst, src)
	gotDeadline, ok := got.Deadline()
	if !ok || !gotDeadline.Equal(deadline) {
		t.Fatalf("destination deadline = %v, %v; want %v, true", gotDeadline, ok, deadline)
	}
	member := baggage.FromContext(got).Member("conversation_id")
	if member.Value() != "conversation-1" || metricLabelEligible(member) {
		t.Fatalf("copied member lost value or property: %q, properties=%v", member.Value(), member.Properties())
	}
	if stats := GetBaggageStats(); stats != (BaggageStats{}) {
		t.Fatalf("CopyBaggage changed usage stats: %+v", stats)
	}
}

func TestBaggageContextHelpers_NilAndEmptyInputs(t *testing.T) {
	if reason, ok := BaggageExactErrorReasonOf(errors.New("other")); ok || reason != "" {
		t.Fatalf("non-exact error reason = %q, %v", reason, ok)
	}

	removed := WithoutBaggageMember(nil, "")
	if removed == nil {
		t.Fatal("WithoutBaggageMember(nil) returned nil")
	}
	if len(GetBaggage(removed)) != 0 {
		t.Fatalf("unexpected baggage after nil removal: %v", GetBaggage(removed))
	}

	copied := CopyBaggage(nil, nil)
	if copied == nil {
		t.Fatal("CopyBaggage(nil, nil) returned nil")
	}
	dst := context.WithValue(context.Background(), baggageContextTestKey{}, "preserved")
	if got := CopyBaggage(dst, context.Background()); got != dst {
		t.Fatal("empty source should return destination unchanged")
	}
	src := WithBaggage(context.Background(), "key", "value")
	if got := GetBaggage(CopyBaggage(nil, src))["key"]; got != "value" {
		t.Fatalf("nil destination copy value = %q", got)
	}
}

func labelsContainKey(labels []string, key string) bool {
	for i := 0; i < len(labels)-1; i += 2 {
		if labels[i] == key {
			return true
		}
	}
	return false
}

func assertExactReason(t *testing.T, err error, want BaggageExactErrorReason) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %q error", want)
	}
	got, ok := BaggageExactErrorReasonOf(err)
	if !ok || got != want {
		t.Fatalf("error reason = %q, %v; want %q, true", got, ok, want)
	}
}
