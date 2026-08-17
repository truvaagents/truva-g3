package orchestration

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const DefaultSkillContentCacheCapacityBytes = 16 * 1024 * 1024

// ImmutableCachedSkillRegistry verifies every exact immutable body and uses an
// optional process-local cache only after authoritative candidate resolution.
// Mutable catalog and published-alias reads are always delegated.
type ImmutableCachedSkillRegistry struct {
	upstream SkillRegistry
	cache    SkillContentCache
}

type skillContentReadEvidence struct {
	CacheOutcome  string
	Source        string
	Attempt       int
	RetryOutcome  string
	ObservedHash  string
	ByteEstimate  int
	TokenEstimate int
	DurationMs    int64
}

type skillContentReadObserver func(skillContentReadEvidence)
type skillContentReadObserverContextKey struct{}

type skillManifestUpstreamReader func(context.Context, SkillVersionRef) (SkillManifest, error)
type skillResourceUpstreamReader func(context.Context, SkillResourceRef) (SkillResource, error)

type skillUpstreamReadInterceptors struct {
	manifest func(
		context.Context,
		SkillVersionRef,
		skillManifestUpstreamReader,
	) (SkillManifest, error)
	resource func(
		context.Context,
		SkillResourceRef,
		skillResourceUpstreamReader,
	) (SkillResource, error)
}

type skillUpstreamReadInterceptorsContextKey struct{}

func withSkillContentReadObserver(
	ctx context.Context,
	observer skillContentReadObserver,
) context.Context {
	return context.WithValue(ctx, skillContentReadObserverContextKey{}, observer)
}

func observeSkillContentRead(ctx context.Context, evidence skillContentReadEvidence) {
	observer, _ := ctx.Value(skillContentReadObserverContextKey{}).(skillContentReadObserver)
	if observer != nil {
		observer(evidence)
	}
}

func withSkillUpstreamReadInterceptors(
	ctx context.Context,
	interceptors skillUpstreamReadInterceptors,
) context.Context {
	return context.WithValue(ctx, skillUpstreamReadInterceptorsContextKey{}, interceptors)
}

func skillUpstreamReadInterceptorsFromContext(ctx context.Context) skillUpstreamReadInterceptors {
	interceptors, _ := ctx.Value(skillUpstreamReadInterceptorsContextKey{}).(skillUpstreamReadInterceptors)
	return interceptors
}

// NewImmutableCachedSkillRegistry constructs the provider-neutral integrity
// decorator. A nil cache means verified direct reads without local caching.
func NewImmutableCachedSkillRegistry(
	upstream SkillRegistry,
	cache SkillContentCache,
) (*ImmutableCachedSkillRegistry, error) {
	if isNilBackendValue(upstream) {
		return nil, fmt.Errorf("%w: upstream skill registry is nil", ErrSkillUnavailable)
	}
	if isNilBackendValue(cache) {
		cache = nil
	}
	if _, disabled := cache.(NoOpSkillContentCache); disabled {
		cache = nil
	}
	return &ImmutableCachedSkillRegistry{upstream: upstream, cache: cache}, nil
}

func (registry *ImmutableCachedSkillRegistry) ListMetadata(
	ctx context.Context,
	filter SkillMetadataFilter,
) ([]SkillMetadata, error) {
	metadata, err := registry.upstream.ListMetadata(ctx, filter)
	if err != nil {
		return nil, err
	}
	return cloneSkillMetadataList(metadata), nil
}

func (registry *ImmutableCachedSkillRegistry) ResolveCandidates(
	ctx context.Context,
	requests []SkillCandidateRequest,
) ([]SkillCandidate, error) {
	candidates, err := registry.upstream.ResolveCandidates(ctx, append([]SkillCandidateRequest(nil), requests...))
	if err != nil {
		return nil, err
	}
	return cloneSkillCandidates(candidates), nil
}

func (registry *ImmutableCachedSkillRegistry) GetManifest(
	ctx context.Context,
	ref SkillVersionRef,
) (SkillManifest, error) {
	if err := ctx.Err(); err != nil {
		return SkillManifest{}, err
	}
	cacheOutcome := "bypass"
	if registry.cache != nil {
		cacheOutcome = "miss"
		cached, found, err := registry.cache.GetManifest(ctx, ref)
		if err == nil && found {
			if verifySkillManifest(ref, cached) == nil {
				observeSkillContentRead(ctx, skillContentReadEvidence{
					CacheOutcome: "hit", Source: "immutable_cache",
					ObservedHash: ref.ManifestHash,
				})
				return cloneSkillManifest(cached), nil
			}
			removeErr := registry.cache.RemoveManifest(ctx, ref)
			observedHash := observedSkillManifestHash(cached)
			manifest, readEvidence, readErr := registry.readManifestUpstream(ctx, ref)
			if readErr != nil && readEvidence.ObservedHash != "" {
				observedHash = readEvidence.ObservedHash
			}
			cacheOutcome := "integrity_mismatch"
			if removeErr != nil || readEvidence.CacheOutcome == "cache_error" {
				cacheOutcome = "cache_error"
			}
			observeSkillContentRead(ctx, skillContentReadEvidence{
				CacheOutcome: cacheOutcome, Source: "immutable_cache",
				Attempt: readEvidence.Attempt, RetryOutcome: skillContentRetryOutcome(readErr),
				ObservedHash: observedHash,
			})
			return manifest, readErr
		}
		if err != nil {
			cacheOutcome = "cache_error"
		}
	}
	manifest, readEvidence, err := registry.readManifestUpstream(ctx, ref)
	if readEvidence.CacheOutcome != "" {
		cacheOutcome = readEvidence.CacheOutcome
	}
	observeSkillContentRead(ctx, skillContentReadEvidence{
		CacheOutcome: cacheOutcome, Source: "verified_registry",
		Attempt: readEvidence.Attempt, RetryOutcome: readEvidence.RetryOutcome,
		ObservedHash: readEvidence.ObservedHash,
	})
	return manifest, err
}

func (registry *ImmutableCachedSkillRegistry) readManifestUpstream(
	ctx context.Context,
	ref SkillVersionRef,
) (SkillManifest, skillContentReadEvidence, error) {
	read := skillManifestUpstreamReader(registry.upstream.GetManifest)
	interceptors := skillUpstreamReadInterceptorsFromContext(ctx)
	evidence := skillContentReadEvidence{Source: "verified_registry"}
	for attempt := 1; attempt <= 2; attempt++ {
		var manifest SkillManifest
		var err error
		if interceptors.manifest != nil {
			manifest, err = interceptors.manifest(ctx, ref, read)
		} else {
			manifest, err = read(ctx, ref)
		}
		evidence.Attempt = attempt
		if err == nil {
			observedHash := observedSkillManifestHash(manifest)
			if attempt == 1 {
				evidence.ObservedHash = observedHash
			}
			err = verifySkillManifest(ref, manifest)
			if err != nil && observedHash != "" {
				evidence.ObservedHash = observedHash
			}
		}
		if err == nil {
			if attempt > 1 {
				evidence.RetryOutcome = "recovered"
			}
			manifest = cloneSkillManifest(manifest)
			if registry.cache != nil {
				if putErr := registry.cache.PutManifest(ctx, ref, cloneSkillManifest(manifest)); putErr != nil {
					evidence.CacheOutcome = "cache_error"
				}
			}
			return manifest, evidence, nil
		}
		if !errors.Is(err, ErrSkillIntegrity) || attempt == 2 {
			if errors.Is(err, ErrSkillIntegrity) {
				evidence.RetryOutcome = "persistent"
			}
			return SkillManifest{}, evidence, err
		}
	}
	return SkillManifest{}, evidence, newSkillDomainError(ErrSkillIntegrity, "verify manifest", ref.Ref)
}

func (registry *ImmutableCachedSkillRegistry) GetResource(
	ctx context.Context,
	ref SkillResourceRef,
) (SkillResource, error) {
	if err := ctx.Err(); err != nil {
		return SkillResource{}, err
	}
	cacheOutcome := "bypass"
	if registry.cache != nil {
		cacheOutcome = "miss"
		cached, found, err := registry.cache.GetResource(ctx, ref)
		if err == nil && found {
			if verifySkillResource(ref, cached) == nil {
				observeSkillContentRead(ctx, skillContentReadEvidence{
					CacheOutcome: "hit", Source: "immutable_cache",
					ObservedHash: ref.ExpectedHash,
				})
				return cloneSkillResource(cached), nil
			}
			removeErr := registry.cache.RemoveResource(ctx, ref)
			observedHash := observedSkillResourceHash(cached)
			resource, readEvidence, readErr := registry.readResourceUpstream(ctx, ref)
			if readErr != nil && readEvidence.ObservedHash != "" {
				observedHash = readEvidence.ObservedHash
			}
			cacheOutcome := "integrity_mismatch"
			if removeErr != nil || readEvidence.CacheOutcome == "cache_error" {
				cacheOutcome = "cache_error"
			}
			observeSkillContentRead(ctx, skillContentReadEvidence{
				CacheOutcome: cacheOutcome, Source: "immutable_cache",
				Attempt: readEvidence.Attempt, RetryOutcome: skillContentRetryOutcome(readErr),
				ObservedHash: observedHash,
			})
			return resource, readErr
		}
		if err != nil {
			cacheOutcome = "cache_error"
		}
	}
	resource, readEvidence, err := registry.readResourceUpstream(ctx, ref)
	if readEvidence.CacheOutcome != "" {
		cacheOutcome = readEvidence.CacheOutcome
	}
	observeSkillContentRead(ctx, skillContentReadEvidence{
		CacheOutcome: cacheOutcome, Source: "verified_registry",
		Attempt: readEvidence.Attempt, RetryOutcome: readEvidence.RetryOutcome,
		ObservedHash: readEvidence.ObservedHash,
	})
	return resource, err
}

func (registry *ImmutableCachedSkillRegistry) readResourceUpstream(
	ctx context.Context,
	ref SkillResourceRef,
) (SkillResource, skillContentReadEvidence, error) {
	read := skillResourceUpstreamReader(registry.upstream.GetResource)
	interceptors := skillUpstreamReadInterceptorsFromContext(ctx)
	evidence := skillContentReadEvidence{Source: "verified_registry"}
	for attempt := 1; attempt <= 2; attempt++ {
		var resource SkillResource
		var err error
		if interceptors.resource != nil {
			resource, err = interceptors.resource(ctx, ref, read)
		} else {
			resource, err = read(ctx, ref)
		}
		evidence.Attempt = attempt
		if err == nil {
			observedHash := observedSkillResourceHash(resource)
			if attempt == 1 {
				evidence.ObservedHash = observedHash
			}
			err = verifySkillResource(ref, resource)
			if err != nil && observedHash != "" {
				evidence.ObservedHash = observedHash
			}
		}
		if err == nil {
			if attempt > 1 {
				evidence.RetryOutcome = "recovered"
			}
			resource = cloneSkillResource(resource)
			if registry.cache != nil {
				if putErr := registry.cache.PutResource(ctx, ref, cloneSkillResource(resource)); putErr != nil {
					evidence.CacheOutcome = "cache_error"
				}
			}
			return resource, evidence, nil
		}
		if !errors.Is(err, ErrSkillIntegrity) || attempt == 2 {
			if errors.Is(err, ErrSkillIntegrity) {
				evidence.RetryOutcome = "persistent"
			}
			return SkillResource{}, evidence, err
		}
	}
	return SkillResource{}, evidence, newSkillDomainError(ErrSkillIntegrity, "verify resource", ref.Skill.Ref)
}

func observedSkillManifestHash(manifest SkillManifest) string {
	hash, err := ComputeSkillManifestHash(manifest)
	if err != nil {
		return ""
	}
	return hash
}

func observedSkillResourceHash(resource SkillResource) string {
	hash, err := ComputeSkillResourceHash(resource)
	if err != nil {
		return ""
	}
	return hash
}

func skillContentRetryOutcome(err error) string {
	if err == nil {
		return "recovered"
	}
	return "persistent"
}

func verifySkillManifest(ref SkillVersionRef, manifest SkillManifest) error {
	if manifest.Ref != ref || !validSkillSHA256(ref.ManifestHash) {
		return newSkillDomainError(ErrSkillIntegrity, "verify manifest", ref.Ref)
	}
	observed, err := ComputeSkillManifestHash(manifest)
	if err != nil || observed != ref.ManifestHash {
		return newSkillDomainError(ErrSkillIntegrity, "verify manifest", ref.Ref)
	}
	return nil
}

func verifySkillResource(ref SkillResourceRef, resource SkillResource) error {
	if resource.Ref != ref || !validSkillSHA256(ref.ExpectedHash) {
		return newSkillDomainError(ErrSkillIntegrity, "verify resource", ref.Skill.Ref)
	}
	observed, err := ComputeSkillResourceHash(resource)
	if err != nil || observed != ref.ExpectedHash {
		return newSkillDomainError(ErrSkillIntegrity, "verify resource", ref.Skill.Ref)
	}
	return nil
}

// NoOpSkillContentCache implements disabled immutable-content caching while
// preserving context cancellation behavior.
type NoOpSkillContentCache struct{}

func (NoOpSkillContentCache) GetManifest(ctx context.Context, _ SkillVersionRef) (SkillManifest, bool, error) {
	return SkillManifest{}, false, ctx.Err()
}
func (NoOpSkillContentCache) PutManifest(ctx context.Context, _ SkillVersionRef, _ SkillManifest) error {
	return ctx.Err()
}
func (NoOpSkillContentCache) RemoveManifest(ctx context.Context, _ SkillVersionRef) error {
	return ctx.Err()
}
func (NoOpSkillContentCache) GetResource(ctx context.Context, _ SkillResourceRef) (SkillResource, bool, error) {
	return SkillResource{}, false, ctx.Err()
}
func (NoOpSkillContentCache) PutResource(ctx context.Context, _ SkillResourceRef, _ SkillResource) error {
	return ctx.Err()
}
func (NoOpSkillContentCache) RemoveResource(ctx context.Context, _ SkillResourceRef) error {
	return ctx.Err()
}

type skillContentCacheEntryKind uint8

const (
	skillManifestCacheEntry skillContentCacheEntryKind = iota + 1
	skillResourceCacheEntry
)

type skillContentCacheEntry struct {
	key      string
	kind     skillContentCacheEntryKind
	bytes    int
	manifest SkillManifest
	resource SkillResource
}

// ByteLRUSkillContentCache is a process-local byte-accounted LRU for exact
// immutable manifests and resources. It has no goroutines or external module
// dependencies.
type ByteLRUSkillContentCache struct {
	mu       sync.Mutex
	capacity int
	used     int
	entries  map[string]*list.Element
	lru      *list.List
}

// NewByteLRUSkillContentCache constructs a cache with a positive byte budget.
func NewByteLRUSkillContentCache(capacityBytes int) (*ByteLRUSkillContentCache, error) {
	if capacityBytes <= 0 {
		return nil, fmt.Errorf("%w: skill content cache capacity must be positive", ErrSkillLimitExceeded)
	}
	return &ByteLRUSkillContentCache{
		capacity: capacityBytes,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
	}, nil
}

func (cache *ByteLRUSkillContentCache) GetManifest(
	ctx context.Context,
	ref SkillVersionRef,
) (SkillManifest, bool, error) {
	if err := ctx.Err(); err != nil {
		return SkillManifest{}, false, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, found := cache.entries[skillManifestCacheKey(ref)]
	if !found || element.Value.(*skillContentCacheEntry).kind != skillManifestCacheEntry {
		return SkillManifest{}, false, nil
	}
	cache.lru.MoveToFront(element)
	return cloneSkillManifest(element.Value.(*skillContentCacheEntry).manifest), true, nil
}

func (cache *ByteLRUSkillContentCache) PutManifest(
	ctx context.Context,
	ref SkillVersionRef,
	manifest SkillManifest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("skill content cache: encode manifest: %w", err)
	}
	entry := &skillContentCacheEntry{
		key: skillManifestCacheKey(ref), kind: skillManifestCacheEntry,
		bytes: len(encoded), manifest: cloneSkillManifest(manifest),
	}
	cache.put(entry)
	return nil
}

func (cache *ByteLRUSkillContentCache) RemoveManifest(ctx context.Context, ref SkillVersionRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cache.remove(skillManifestCacheKey(ref))
	return nil
}

func (cache *ByteLRUSkillContentCache) GetResource(
	ctx context.Context,
	ref SkillResourceRef,
) (SkillResource, bool, error) {
	if err := ctx.Err(); err != nil {
		return SkillResource{}, false, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, found := cache.entries[skillResourceCacheKey(ref)]
	if !found || element.Value.(*skillContentCacheEntry).kind != skillResourceCacheEntry {
		return SkillResource{}, false, nil
	}
	cache.lru.MoveToFront(element)
	return cloneSkillResource(element.Value.(*skillContentCacheEntry).resource), true, nil
}

func (cache *ByteLRUSkillContentCache) PutResource(
	ctx context.Context,
	ref SkillResourceRef,
	resource SkillResource,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(resource)
	if err != nil {
		return fmt.Errorf("skill content cache: encode resource: %w", err)
	}
	entry := &skillContentCacheEntry{
		key: skillResourceCacheKey(ref), kind: skillResourceCacheEntry,
		bytes: len(encoded), resource: cloneSkillResource(resource),
	}
	cache.put(entry)
	return nil
}

func (cache *ByteLRUSkillContentCache) RemoveResource(ctx context.Context, ref SkillResourceRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cache.remove(skillResourceCacheKey(ref))
	return nil
}

func (cache *ByteLRUSkillContentCache) put(entry *skillContentCacheEntry) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if existing, found := cache.entries[entry.key]; found {
		cache.removeElement(existing)
	}
	if entry.bytes > cache.capacity {
		return
	}
	for cache.used+entry.bytes > cache.capacity {
		oldest := cache.lru.Back()
		if oldest == nil {
			break
		}
		cache.removeElement(oldest)
	}
	element := cache.lru.PushFront(entry)
	cache.entries[entry.key] = element
	cache.used += entry.bytes
}

func (cache *ByteLRUSkillContentCache) remove(key string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, found := cache.entries[key]; found {
		cache.removeElement(element)
	}
}

func (cache *ByteLRUSkillContentCache) removeElement(element *list.Element) {
	entry := element.Value.(*skillContentCacheEntry)
	delete(cache.entries, entry.key)
	cache.lru.Remove(element)
	cache.used -= entry.bytes
}

func skillManifestCacheKey(ref SkillVersionRef) string {
	return "m\x00" + ref.Ref.Namespace + "\x00" + ref.Ref.Name + "\x00" +
		fmt.Sprintf("%d", ref.Version) + "\x00" + ref.ManifestHash
}

func skillResourceCacheKey(ref SkillResourceRef) string {
	return "r\x00" + ref.Skill.Ref.Namespace + "\x00" + ref.Skill.Ref.Name + "\x00" +
		fmt.Sprintf("%d", ref.Skill.Version) + "\x00" + ref.Skill.ManifestHash + "\x00" +
		ref.Name + "\x00" + ref.ExpectedHash
}

func cloneSkillManifest(value SkillManifest) SkillManifest {
	cloned := value
	cloned.Domains = append([]string(nil), value.Domains...)
	cloned.Tags = append([]string(nil), value.Tags...)
	cloned.PlanningInstructions = append([]string(nil), value.PlanningInstructions...)
	cloned.ResponseInstructions = append([]string(nil), value.ResponseInstructions...)
	cloned.ToolHints = append([]string(nil), value.ToolHints...)
	cloned.Resources = make([]SkillResourceMetadata, len(value.Resources))
	for index, resource := range value.Resources {
		cloned.Resources[index] = resource
		cloned.Resources[index].AppliesTo = append([]SkillResourceScope(nil), resource.AppliesTo...)
	}
	return cloned
}

func cloneSkillResource(value SkillResource) SkillResource {
	return value
}

func cloneSkillMetadataList(values []SkillMetadata) []SkillMetadata {
	cloned := make([]SkillMetadata, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Domains = append([]string(nil), value.Domains...)
		cloned[index].Tags = append([]string(nil), value.Tags...)
	}
	return cloned
}

func cloneSkillCandidates(values []SkillCandidate) []SkillCandidate {
	cloned := make([]SkillCandidate, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Metadata.Domains = append([]string(nil), value.Metadata.Domains...)
		cloned[index].Metadata.Tags = append([]string(nil), value.Metadata.Tags...)
	}
	return cloned
}

var (
	_ SkillRegistry     = (*ImmutableCachedSkillRegistry)(nil)
	_ SkillContentCache = NoOpSkillContentCache{}
	_ SkillContentCache = (*ByteLRUSkillContentCache)(nil)
)
