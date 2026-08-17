package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestImmutableCachedSkillRegistryDelegatesMutableReads(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "first")
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	cache, err := NewByteLRUSkillContentCache(1024 * 1024)
	if err != nil {
		t.Fatalf("NewByteLRUSkillContentCache() error = %v", err)
	}
	registry, err := NewImmutableCachedSkillRegistry(upstream, cache)
	if err != nil {
		t.Fatalf("NewImmutableCachedSkillRegistry() error = %v", err)
	}
	requests := []SkillCandidateRequest{{Ref: manifest.Ref.Ref, RequestedVersion: "published"}}
	for range 2 {
		candidates, err := registry.ResolveCandidates(t.Context(), requests)
		if err != nil || len(candidates) != 1 || candidates[0].Resolved != manifest.Ref {
			t.Fatalf("ResolveCandidates() = %#v, %v", candidates, err)
		}
		metadata, err := registry.ListMetadata(t.Context(), SkillMetadataFilter{})
		if err != nil || len(metadata) != 1 {
			t.Fatalf("ListMetadata() = %#v, %v", metadata, err)
		}
	}
	if upstream.resolveCalls != 2 || upstream.listCalls != 2 {
		t.Fatalf("mutable read calls = resolve %d, list %d; want 2 each", upstream.resolveCalls, upstream.listCalls)
	}
}

func TestImmutableCachedSkillRegistryCachesVerifiedExactBodies(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "verified")
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	cache, _ := NewByteLRUSkillContentCache(1024 * 1024)
	registry, _ := NewImmutableCachedSkillRegistry(upstream, cache)

	firstManifest, err := registry.GetManifest(t.Context(), manifest.Ref)
	if err != nil {
		t.Fatalf("GetManifest(first) error = %v", err)
	}
	firstManifest.PlanningInstructions[0] = "caller mutation"
	secondManifest, err := registry.GetManifest(t.Context(), manifest.Ref)
	if err != nil || secondManifest.PlanningInstructions[0] != manifest.PlanningInstructions[0] {
		t.Fatalf("GetManifest(second) = %#v, %v", secondManifest, err)
	}
	if upstream.manifestCalls != 1 {
		t.Fatalf("manifest upstream calls = %d, want 1", upstream.manifestCalls)
	}

	firstResource, err := registry.GetResource(t.Context(), resource.Ref)
	if err != nil {
		t.Fatalf("GetResource(first) error = %v", err)
	}
	firstResource.Content = "caller mutation"
	secondResource, err := registry.GetResource(t.Context(), resource.Ref)
	if err != nil || secondResource.Content != resource.Content {
		t.Fatalf("GetResource(second) = %#v, %v", secondResource, err)
	}
	if upstream.resourceCalls != 1 {
		t.Fatalf("resource upstream calls = %d, want 1", upstream.resourceCalls)
	}
}

func TestImmutableCachedSkillRegistryReportsMissHitAndCorruptionReread(t *testing.T) {
	manifest, _ := cacheTestSkillContent(t, "evidence")
	upstream := &cacheTestSkillRegistry{manifest: manifest}
	cache, _ := NewByteLRUSkillContentCache(1024 * 1024)
	registry, _ := NewImmutableCachedSkillRegistry(upstream, cache)
	var evidence []skillContentReadEvidence
	ctx := withSkillContentReadObserver(t.Context(), func(value skillContentReadEvidence) {
		evidence = append(evidence, value)
	})
	if _, err := registry.GetManifest(ctx, manifest.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.GetManifest(ctx, manifest.Ref); err != nil {
		t.Fatal(err)
	}
	poisoned := cloneSkillManifest(manifest)
	poisoned.PlanningInstructions[0] += " corrupted"
	if err := cache.PutManifest(t.Context(), manifest.Ref, poisoned); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.GetManifest(ctx, manifest.Ref); err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 3 || evidence[0].CacheOutcome != "miss" ||
		evidence[1].CacheOutcome != "hit" || evidence[1].Source != "immutable_cache" ||
		evidence[2].CacheOutcome != "integrity_mismatch" ||
		evidence[2].RetryOutcome != "recovered" || upstream.manifestCalls != 2 {
		t.Fatalf("content read evidence = %#v; upstream calls = %d", evidence, upstream.manifestCalls)
	}
}

func TestImmutableCachedSkillRegistryEvictsCorruptionAndRereadsExactlyOnce(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "reread")
	poisonedManifest := cloneSkillManifest(manifest)
	poisonedManifest.PlanningInstructions[0] += " corrupted"
	poisonedResource := resource
	poisonedResource.Content += " corrupted"
	cache := &cacheTestSkillContentCache{
		manifest: poisonedManifest, manifestFound: true,
		resource: poisonedResource, resourceFound: true,
	}
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	registry, _ := NewImmutableCachedSkillRegistry(upstream, cache)

	gotManifest, err := registry.GetManifest(t.Context(), manifest.Ref)
	if err != nil || gotManifest.Ref != manifest.Ref {
		t.Fatalf("GetManifest() = %#v, %v", gotManifest, err)
	}
	gotResource, err := registry.GetResource(t.Context(), resource.Ref)
	if err != nil || gotResource.Ref != resource.Ref {
		t.Fatalf("GetResource() = %#v, %v", gotResource, err)
	}
	if cache.manifestRemoves != 1 || cache.resourceRemoves != 1 ||
		upstream.manifestCalls != 1 || upstream.resourceCalls != 1 {
		t.Fatalf("corruption recovery = cache removals %d/%d upstream %d/%d",
			cache.manifestRemoves, cache.resourceRemoves, upstream.manifestCalls, upstream.resourceCalls)
	}
}

func TestImmutableCachedSkillRegistryRejectsPersistentUpstreamCorruption(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "persistent")
	manifest.PlanningInstructions[0] += " corrupted"
	resource.Content += " corrupted"
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	registry, _ := NewImmutableCachedSkillRegistry(upstream, nil)
	var evidence []skillContentReadEvidence
	ctx := withSkillContentReadObserver(t.Context(), func(value skillContentReadEvidence) {
		evidence = append(evidence, value)
	})

	if _, err := registry.GetManifest(ctx, manifest.Ref); !errors.Is(err, ErrSkillIntegrity) {
		t.Fatalf("GetManifest(corrupt) error = %v, want ErrSkillIntegrity", err)
	}
	if _, err := registry.GetResource(ctx, resource.Ref); !errors.Is(err, ErrSkillIntegrity) {
		t.Fatalf("GetResource(corrupt) error = %v, want ErrSkillIntegrity", err)
	}
	if upstream.manifestCalls != 2 || upstream.resourceCalls != 2 {
		t.Fatalf("persistent integrity reads = %d/%d, want one bounded reread each",
			upstream.manifestCalls, upstream.resourceCalls)
	}
	if len(evidence) != 2 {
		t.Fatalf("integrity evidence = %#v", evidence)
	}
	for _, item := range evidence {
		if item.Attempt != 2 || item.RetryOutcome != "persistent" || item.ObservedHash == "" {
			t.Fatalf("persistent integrity evidence = %#v", item)
		}
	}
}

func TestImmutableCachedSkillRegistryRecoversFromFirstUpstreamIntegrityMismatch(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "upstream-recovery")
	corruptManifest := cloneSkillManifest(manifest)
	corruptManifest.PlanningInstructions[0] += " corrupted"
	corruptResource := resource
	corruptResource.Content += " corrupted"
	upstream := &cacheTestSkillRegistry{
		manifest: manifest, resource: resource,
		manifestResponses: []SkillManifest{corruptManifest, manifest},
		resourceResponses: []SkillResource{corruptResource, resource},
	}
	registry, _ := NewImmutableCachedSkillRegistry(upstream, nil)
	var evidence []skillContentReadEvidence
	ctx := withSkillContentReadObserver(t.Context(), func(value skillContentReadEvidence) {
		evidence = append(evidence, value)
	})

	if got, err := registry.GetManifest(ctx, manifest.Ref); err != nil || got.Ref != manifest.Ref {
		t.Fatalf("GetManifest(recovered) = %#v, %v", got, err)
	}
	if got, err := registry.GetResource(ctx, resource.Ref); err != nil || got.Ref != resource.Ref {
		t.Fatalf("GetResource(recovered) = %#v, %v", got, err)
	}
	if upstream.manifestCalls != 2 || upstream.resourceCalls != 2 || len(evidence) != 2 {
		t.Fatalf("recovery calls/evidence = %d/%d %#v",
			upstream.manifestCalls, upstream.resourceCalls, evidence)
	}
	for index, item := range evidence {
		expectedHash := manifest.Ref.ManifestHash
		if index == 1 {
			expectedHash = resource.Ref.ExpectedHash
		}
		if item.Attempt != 2 || item.RetryOutcome != "recovered" ||
			item.ObservedHash == "" || item.ObservedHash == expectedHash {
			t.Fatalf("recovered integrity evidence = %#v", item)
		}
	}
}

func TestImmutableCachedSkillRegistryCacheErrorsDegradeToVerifiedUpstream(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "cache-error")
	cache := &cacheTestSkillContentCache{getError: errors.New("cache unavailable"), putError: errors.New("cache full")}
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	registry, _ := NewImmutableCachedSkillRegistry(upstream, cache)
	var evidence []skillContentReadEvidence
	ctx := withSkillContentReadObserver(t.Context(), func(value skillContentReadEvidence) {
		evidence = append(evidence, value)
	})

	if _, err := registry.GetManifest(ctx, manifest.Ref); err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}
	if _, err := registry.GetResource(ctx, resource.Ref); err != nil {
		t.Fatalf("GetResource() error = %v", err)
	}
	if upstream.manifestCalls != 1 || upstream.resourceCalls != 1 {
		t.Fatalf("upstream calls = %d/%d", upstream.manifestCalls, upstream.resourceCalls)
	}
	if len(evidence) != 2 || evidence[0].CacheOutcome != "cache_error" ||
		evidence[1].CacheOutcome != "cache_error" {
		t.Fatalf("cache-error evidence = %#v", evidence)
	}
}

func TestImmutableCachedSkillRegistryReportsFailedCorruptEntryRemoval(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "remove-error")
	poisoned := cloneSkillManifest(manifest)
	poisoned.PlanningInstructions[0] += " corrupted"
	cache := &cacheTestSkillContentCache{
		manifest: poisoned, manifestFound: true,
		removeError: errors.New("cache removal failed"),
	}
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	registry, _ := NewImmutableCachedSkillRegistry(upstream, cache)
	var evidence skillContentReadEvidence
	ctx := withSkillContentReadObserver(t.Context(), func(value skillContentReadEvidence) {
		evidence = value
	})

	if _, err := registry.GetManifest(ctx, manifest.Ref); err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}
	if evidence.CacheOutcome != "cache_error" || evidence.Source != "immutable_cache" ||
		evidence.RetryOutcome != "recovered" {
		t.Fatalf("remove-error evidence = %#v", evidence)
	}
}

func TestImmutableCachedSkillRegistryPropagatesContextCancellation(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "cancellation")
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	registry, _ := NewImmutableCachedSkillRegistry(upstream, NoOpSkillContentCache{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := registry.GetManifest(ctx, manifest.Ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetManifest(cancelled) error = %v", err)
	}
	if _, err := registry.GetResource(ctx, resource.Ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetResource(cancelled) error = %v", err)
	}
	if upstream.manifestCalls != 0 || upstream.resourceCalls != 0 {
		t.Fatalf("cancelled reads reached upstream: %d/%d", upstream.manifestCalls, upstream.resourceCalls)
	}
}

func TestNoOpSkillContentCachePreservesDisabledAndCancellationSemantics(t *testing.T) {
	cache := NoOpSkillContentCache{}
	manifestRef := SkillVersionRef{Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 1}
	resourceRef := SkillResourceRef{Skill: manifestRef, Name: "details"}

	if _, found, err := cache.GetManifest(t.Context(), manifestRef); err != nil || found {
		t.Fatalf("GetManifest() = found %v, error %v", found, err)
	}
	if err := cache.PutManifest(t.Context(), manifestRef, SkillManifest{}); err != nil {
		t.Fatalf("PutManifest() error = %v", err)
	}
	if err := cache.RemoveManifest(t.Context(), manifestRef); err != nil {
		t.Fatalf("RemoveManifest() error = %v", err)
	}
	if _, found, err := cache.GetResource(t.Context(), resourceRef); err != nil || found {
		t.Fatalf("GetResource() = found %v, error %v", found, err)
	}
	if err := cache.PutResource(t.Context(), resourceRef, SkillResource{}); err != nil {
		t.Fatalf("PutResource() error = %v", err)
	}
	if err := cache.RemoveResource(t.Context(), resourceRef); err != nil {
		t.Fatalf("RemoveResource() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	operations := []struct {
		name string
		run  func() error
	}{
		{"get manifest", func() error { _, _, err := cache.GetManifest(ctx, manifestRef); return err }},
		{"put manifest", func() error { return cache.PutManifest(ctx, manifestRef, SkillManifest{}) }},
		{"remove manifest", func() error { return cache.RemoveManifest(ctx, manifestRef) }},
		{"get resource", func() error { _, _, err := cache.GetResource(ctx, resourceRef); return err }},
		{"put resource", func() error { return cache.PutResource(ctx, resourceRef, SkillResource{}) }},
		{"remove resource", func() error { return cache.RemoveResource(ctx, resourceRef) }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestByteLRUSkillContentCacheAccountsBytesAndEvictsLeastRecent(t *testing.T) {
	firstManifest, _ := cacheTestSkillContent(t, "first")
	secondManifest, _ := cacheTestSkillContent(t, "second")
	secondManifest.Ref.Version++
	secondManifest.PlanningInstructions[0] = strings.ReplaceAll(secondManifest.PlanningInstructions[0], "first", "second")
	secondManifest.Ref.ManifestHash = ""
	secondHash, err := ComputeSkillManifestHash(secondManifest)
	if err != nil {
		t.Fatalf("ComputeSkillManifestHash(second) error = %v", err)
	}
	secondManifest.Ref.ManifestHash = secondHash
	firstEncoded, _ := json.Marshal(firstManifest)
	secondEncoded, _ := json.Marshal(secondManifest)
	capacity := max(len(firstEncoded), len(secondEncoded))
	cache, err := NewByteLRUSkillContentCache(capacity)
	if err != nil {
		t.Fatalf("NewByteLRUSkillContentCache() error = %v", err)
	}
	if err := cache.PutManifest(t.Context(), firstManifest.Ref, firstManifest); err != nil {
		t.Fatal(err)
	}
	if err := cache.PutManifest(t.Context(), secondManifest.Ref, secondManifest); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.GetManifest(t.Context(), firstManifest.Ref); err != nil || found {
		t.Fatalf("first cache read = found %v, error %v; want evicted", found, err)
	}
	if _, found, err := cache.GetManifest(t.Context(), secondManifest.Ref); err != nil || !found {
		t.Fatalf("second cache read = found %v, error %v", found, err)
	}

	oversized, err := NewByteLRUSkillContentCache(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := oversized.PutManifest(t.Context(), firstManifest.Ref, firstManifest); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := oversized.GetManifest(t.Context(), firstManifest.Ref); found {
		t.Fatal("oversized cache entry was admitted")
	}
}

func TestByteLRUSkillContentCacheRemovesResource(t *testing.T) {
	_, resource := cacheTestSkillContent(t, "remove-resource")
	cache, err := NewByteLRUSkillContentCache(1024 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.PutResource(t.Context(), resource.Ref, resource); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.GetResource(t.Context(), resource.Ref); err != nil || !found {
		t.Fatalf("GetResource(before remove) = found %v, error %v", found, err)
	}
	if err := cache.RemoveResource(t.Context(), resource.Ref); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.GetResource(t.Context(), resource.Ref); err != nil || found {
		t.Fatalf("GetResource(after remove) = found %v, error %v", found, err)
	}
}

func TestSkillContentCacheConstructorsRejectInvalidDependencies(t *testing.T) {
	if _, err := NewImmutableCachedSkillRegistry(nil, nil); !errors.Is(err, ErrSkillUnavailable) {
		t.Fatalf("NewImmutableCachedSkillRegistry(nil) error = %v", err)
	}
	if _, err := NewByteLRUSkillContentCache(0); !errors.Is(err, ErrSkillLimitExceeded) {
		t.Fatalf("NewByteLRUSkillContentCache(0) error = %v", err)
	}
	if DefaultSkillContentCacheCapacityBytes != 16*1024*1024 {
		t.Fatalf("DefaultSkillContentCacheCapacityBytes = %d", DefaultSkillContentCacheCapacityBytes)
	}
}

type cacheTestSkillRegistry struct {
	mu                sync.Mutex
	manifest          SkillManifest
	resource          SkillResource
	listCalls         int
	resolveCalls      int
	manifestCalls     int
	resourceCalls     int
	manifestResponses []SkillManifest
	resourceResponses []SkillResource
}

func (registry *cacheTestSkillRegistry) ListMetadata(context.Context, SkillMetadataFilter) ([]SkillMetadata, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.listCalls++
	return []SkillMetadata{{
		Ref: registry.manifest.Ref.Ref, DisplayName: registry.manifest.DisplayName,
		Description: registry.manifest.Description, Domains: registry.manifest.Domains,
		Tags: registry.manifest.Tags, PublishedVersion: registry.manifest.Ref.Version,
		Status: SkillPublicationPublished,
	}}, nil
}

func (registry *cacheTestSkillRegistry) ResolveCandidates(context.Context, []SkillCandidateRequest) ([]SkillCandidate, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.resolveCalls++
	return []SkillCandidate{{
		Ref: registry.manifest.Ref.Ref, RequestedVersion: "published",
		Resolved: registry.manifest.Ref, Status: SkillCandidateResolved,
		Metadata: SkillMetadata{
			Ref: registry.manifest.Ref.Ref, DisplayName: registry.manifest.DisplayName,
			Description: registry.manifest.Description, PublishedVersion: registry.manifest.Ref.Version,
			Status: SkillPublicationPublished,
		},
	}}, nil
}

func (registry *cacheTestSkillRegistry) GetManifest(context.Context, SkillVersionRef) (SkillManifest, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.manifestCalls++
	if len(registry.manifestResponses) > 0 {
		index := min(registry.manifestCalls-1, len(registry.manifestResponses)-1)
		return cloneSkillManifest(registry.manifestResponses[index]), nil
	}
	return cloneSkillManifest(registry.manifest), nil
}

func (registry *cacheTestSkillRegistry) GetResource(context.Context, SkillResourceRef) (SkillResource, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.resourceCalls++
	if len(registry.resourceResponses) > 0 {
		index := min(registry.resourceCalls-1, len(registry.resourceResponses)-1)
		return registry.resourceResponses[index], nil
	}
	return registry.resource, nil
}

type cacheTestSkillContentCache struct {
	manifest        SkillManifest
	manifestFound   bool
	resource        SkillResource
	resourceFound   bool
	getError        error
	putError        error
	removeError     error
	manifestRemoves int
	resourceRemoves int
}

func (cache *cacheTestSkillContentCache) GetManifest(context.Context, SkillVersionRef) (SkillManifest, bool, error) {
	return cache.manifest, cache.manifestFound, cache.getError
}
func (cache *cacheTestSkillContentCache) PutManifest(_ context.Context, _ SkillVersionRef, manifest SkillManifest) error {
	cache.manifest = manifest
	cache.manifestFound = true
	return cache.putError
}
func (cache *cacheTestSkillContentCache) RemoveManifest(context.Context, SkillVersionRef) error {
	cache.manifestFound = false
	cache.manifestRemoves++
	return cache.removeError
}
func (cache *cacheTestSkillContentCache) GetResource(context.Context, SkillResourceRef) (SkillResource, bool, error) {
	return cache.resource, cache.resourceFound, cache.getError
}
func (cache *cacheTestSkillContentCache) PutResource(_ context.Context, _ SkillResourceRef, resource SkillResource) error {
	cache.resource = resource
	cache.resourceFound = true
	return cache.putError
}
func (cache *cacheTestSkillContentCache) RemoveResource(context.Context, SkillResourceRef) error {
	cache.resourceFound = false
	cache.resourceRemoves++
	return cache.removeError
}

func cacheTestSkillContent(t *testing.T, marker string) (SkillManifest, SkillResource) {
	t.Helper()
	ref := SkillVersionRef{Ref: SkillRef{Namespace: "travel", Name: "weather-assessment"}, Version: 1}
	resource := SkillResource{
		Ref:         SkillResourceRef{Skill: ref, Name: "details"},
		ContentType: "text/plain", Content: "Resource content for " + marker + ".",
	}
	resourceHash, err := ComputeSkillResourceHash(resource)
	if err != nil {
		t.Fatalf("ComputeSkillResourceHash() error = %v", err)
	}
	resource.Ref.ExpectedHash = resourceHash
	manifest := SkillManifest{
		Ref: ref, DisplayName: "Weather Assessment",
		Description: "Assess conditions. Use when a trip depends on weather.",
		Domains:     []string{"travel"}, Tags: []string{"weather"},
		PlanningInstructions: []string{"Assess conditions for " + marker + "."},
		Resources: []SkillResourceMetadata{{
			Name: "details", Description: "Conditional details.",
			LoadWhen: "Detailed conditions are requested.", AppliesTo: []SkillResourceScope{SkillResourceContinuation},
			ContentType: "text/plain", ResourceHash: resourceHash,
		}},
	}
	manifestHash, err := ComputeSkillManifestHash(manifest)
	if err != nil {
		t.Fatalf("ComputeSkillManifestHash() error = %v", err)
	}
	manifest.Ref.ManifestHash = manifestHash
	resource.Ref.Skill.ManifestHash = manifestHash
	return manifest, resource
}

var _ SkillRegistry = (*cacheTestSkillRegistry)(nil)
var _ SkillContentCache = (*cacheTestSkillContentCache)(nil)
