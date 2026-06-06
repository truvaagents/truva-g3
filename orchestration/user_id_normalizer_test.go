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

// Storage and recall agree end-to-end: a fact stored under the normalized "joe"
// is recalled by a later "JOE" request when both use the same normalizer.
func TestExtractionAndEnrichment_ShareCanonicalBucket(t *testing.T) {
	mem := newTestUserMemory()
	ctx := context.Background()

	normalized := NormalizeUserIDLowercaseTrim("Joe")
	require.Equal(t, "joe", normalized)
	require.NoError(t, mem.Remember(ctx, normalized, core.UserFact{
		FactID: "f1", Namespace: "travel", Category: "preference",
		Content: "User prefers aisle seats", Source: core.SourceExplicit, Confidence: 0.95,
	}))

	hook := NewUserMemoryEnrichmentHook(mem, "travel", &core.NoOpLogger{},
		WithUserMemoryEnrichmentUserIDNormalizer(NormalizeUserIDLowercaseTrim),
	)
	pctx := &core.PipelineContext{
		Request:  "aisle seats",
		Metadata: map[string]interface{}{"user_id": "JOE"},
	}
	_, err := hook.BeforePlanning(ctx, pctx)
	require.NoError(t, err)
	require.NotNil(t, pctx.Enrichments)
	profile, _ := pctx.Enrichments[core.EnrichmentUserProfile].(string)
	assert.Contains(t, profile, "User prefers aisle seats")
}
