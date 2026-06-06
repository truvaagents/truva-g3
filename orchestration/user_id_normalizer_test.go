package orchestration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestNormalizeUserIDLowercaseTrim(t *testing.T) {
	cases := map[string]string{
		"Joe":      "joe",
		" joe ":    "joe",
		"JOE":      "joe",
		"Neelabh":  "neelabh",
		"neelabh":  "neelabh",
		"\tUser\n": "user",
		"":         "",
		"  ":       "",
	}
	for in, want := range cases {
		assert.Equalf(t, want, NormalizeUserIDLowercaseTrim(in), "input %q", in)
	}
}

func TestIdentityUserIDNormalizer(t *testing.T) {
	assert.Equal(t, "Joe", IdentityUserIDNormalizer("Joe"))
	assert.Equal(t, " Joe ", IdentityUserIDNormalizer(" Joe "))
}

// ─── Chokepoint integration: recall reads the canonical bucket ───────────────

// With a lowercase-trim normalizer, a request carrying "Joe" recalls facts that
// were stored under the canonical "joe" — the case-split is folded away.
func TestEnrichmentHook_NormalizesUserIDOnRecall(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	require.NoError(t, mem.Remember(ctx, "joe", core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "preference",
		Content: "User prefers window seats", Source: core.SourceExplicit, Confidence: 0.95,
	}))

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{},
		WithUserMemoryEnrichmentUserIDNormalizer(NormalizeUserIDLowercaseTrim),
	)
	pctx := &core.PipelineContext{
		Request:  "window seats",
		Metadata: map[string]interface{}{"user_id": "Joe"},
	}

	_, err := hook.BeforePlanning(ctx, pctx)
	require.NoError(t, err)
	require.NotNil(t, pctx.Enrichments)
	profile, _ := pctx.Enrichments[core.EnrichmentUserProfile].(string)
	assert.Contains(t, profile, "User prefers window seats",
		"mixed-case id should recall facts stored under the canonical id")
}

// The default (no normalizer) preserves case-sensitive isolation: "Joe" does
// NOT see facts stored under "joe". Guards against an accidental behavior change.
func TestEnrichmentHook_DefaultIsCaseSensitive(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	require.NoError(t, mem.Remember(ctx, "joe", core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "preference",
		Content: "User prefers window seats", Source: core.SourceExplicit, Confidence: 0.95,
	}))

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{})
	pctx := &core.PipelineContext{
		Request:  "window seats",
		Metadata: map[string]interface{}{"user_id": "Joe"},
	}

	_, err := hook.BeforePlanning(ctx, pctx)
	require.NoError(t, err)
	assert.Nil(t, pctx.Enrichments, "case-sensitive default must not cross buckets")
}

// The extraction hook normalizes user_id before storage: a fact extracted for
// a mixed-case "Joe" lands in the canonical "joe" bucket, not "Joe".
func TestExtractionHook_NormalizesUserIDOnStorage(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	extractor := newSlowFactExtractor([]core.UserFact{
		{Content: "User prefers aisle seats", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}, 0)

	// Synchronous (Layer-3 default): AfterSynthesis blocks until storage.
	hook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{},
		extractor, &addAllReconciler{},
		WithUserExtractionUserIDNormalizer(NormalizeUserIDLowercaseTrim),
	)
	t.Cleanup(func() { _ = hook.Close() })

	_, err := hook.AfterSynthesis(ctx, &core.PipelineContext{
		Request:  "book me an aisle seat",
		Metadata: map[string]interface{}{"user_id": "Joe"},
	}, "aisle seat booked")
	require.NoError(t, err)

	canonical, _ := mem.Recall(ctx, "joe", "travel", "", 10)
	require.NotEmpty(t, canonical, "extraction must store under the normalized user id")
	assert.Equal(t, "User prefers aisle seats", canonical[0].Content)

	raw, _ := mem.Recall(ctx, "Joe", "travel", "", 10)
	assert.Empty(t, raw, "nothing should be stored under the raw mixed-case id")
}

// End-to-end: a fact STORED via the extraction hook for "Joe" is RECALLED via
// the enrichment hook for "JOE" — both hooks fold to the same canonical bucket.
// Exercises both chokepoints (AfterSynthesis storage + BeforePlanning recall),
// not a manual mem.Remember seed.
func TestExtractionAndEnrichment_ShareCanonicalBucket(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()
	norm := NormalizeUserIDLowercaseTrim

	// Storage side: extraction hook persists a fact for "Joe".
	extractor := newSlowFactExtractor([]core.UserFact{
		{Content: "User prefers aisle seats", Category: "preference", Source: core.SourceExplicit, Confidence: 0.95},
	}, 0)
	extractHook := NewUserMemoryExtractionHook(
		mem, nil, nil, "travel", &core.NoOpLogger{},
		extractor, &addAllReconciler{},
		WithUserExtractionUserIDNormalizer(norm),
	)
	t.Cleanup(func() { _ = extractHook.Close() })
	_, err := extractHook.AfterSynthesis(ctx, &core.PipelineContext{
		Request:  "book me an aisle seat",
		Metadata: map[string]interface{}{"user_id": "Joe"},
	}, "aisle seat booked")
	require.NoError(t, err)

	// Recall side: enrichment hook for a different casing ("JOE") finds it.
	enrichHook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{},
		WithUserMemoryEnrichmentUserIDNormalizer(norm),
	)
	pctx := &core.PipelineContext{
		Request:  "aisle seats",
		Metadata: map[string]interface{}{"user_id": "JOE"},
	}
	_, err = enrichHook.BeforePlanning(ctx, pctx)
	require.NoError(t, err)
	require.NotNil(t, pctx.Enrichments)
	profile, _ := pctx.Enrichments[core.EnrichmentUserProfile].(string)
	assert.Contains(t, profile, "User prefers aisle seats",
		"a fact stored for 'Joe' must be recalled for 'JOE' via the shared canonical bucket")
}
