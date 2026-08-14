package redisprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

const (
	defaultSkillStoreKeyPrefix = "truvag3:skills"
	maxSkillStoreBatchSize     = 256
	defaultSkillListLimit      = 100
	maxSkillListLimit          = 100
	maxSkillStoreTxRetries     = 32
)

// SkillStore is the included Redis implementation of the provider-neutral
// skill runtime and administration contracts. Its key schema and transaction
// mechanics are deliberately private to this package.
type SkillStore struct {
	client    redis.UniversalClient
	keyPrefix string
	logger    core.Logger
	now       func() time.Time
}

type SkillStoreOption interface{ applySkillStore(*SkillStore) error }
type skillStoreOption func(*SkillStore) error

func (option skillStoreOption) applySkillStore(store *SkillStore) error { return option(store) }

func WithSkillStoreKeyPrefix(prefix string) SkillStoreOption {
	return skillStoreOption(func(store *SkillStore) error {
		prefix = strings.TrimSpace(prefix)
		if !namespacePattern.MatchString(prefix) {
			return fmt.Errorf("redisprovider: skill key prefix must match %s", namespacePattern.String())
		}
		store.keyPrefix = prefix
		return nil
	})
}

func WithSkillStoreLogger(logger core.Logger) SkillStoreOption {
	return skillStoreOption(func(store *SkillStore) error {
		if nilSkillStoreLogger(logger) {
			return fmt.Errorf("redisprovider: skill store logger is nil")
		}
		store.logger = logger
		return nil
	})
}

func NewSkillStore(client redis.UniversalClient, options ...SkillStoreOption) (*SkillStore, error) {
	if nilRedisClient(client) {
		return nil, fmt.Errorf("redisprovider: skill store client is required")
	}
	store := &SkillStore{
		client: client, keyPrefix: defaultSkillStoreKeyPrefix,
		logger: &core.NoOpLogger{}, now: time.Now,
	}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("redisprovider: skill store option %d is nil", index)
		}
		if err := option.applySkillStore(store); err != nil {
			return nil, err
		}
	}
	return store, nil
}

type storedSkillPublished struct {
	Ref           orchestration.SkillVersionRef `json:"ref"`
	Metadata      orchestration.SkillMetadata   `json:"metadata"`
	RevisionToken string                        `json:"revision_token"`
}

// storedSkillCandidate is the immutable body-free exact-version lookup record.
// Candidate resolution must never load the full authoring representation.
type storedSkillCandidate struct {
	Ref      orchestration.SkillVersionRef `json:"ref"`
	Metadata orchestration.SkillMetadata   `json:"metadata"`
}

type storedSkillRevision struct {
	Representation orchestration.SkillRevisionRepresentation `json:"representation"`
	Summary        orchestration.SkillRevisionSummary        `json:"summary"`
	RevisionToken  string                                    `json:"revision_token"`
}

type storedSkillIdempotency struct {
	Package       orchestration.ValidatedSkillPackage   `json:"package"`
	Result        orchestration.PutPublishedSkillResult `json:"result"`
	RevisionToken string                                `json:"revision_token"`
}

func (store *SkillStore) ListMetadata(
	ctx context.Context,
	filter orchestration.SkillMetadataFilter,
) ([]orchestration.SkillMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultSkillListLimit
	}
	if limit > maxSkillListLimit {
		limit = maxSkillListLimit
	}
	identities, err := store.client.SMembers(ctx, store.catalogKey()).Result()
	if err != nil {
		return nil, store.backendError(ctx, "list metadata", orchestration.ErrSkillUnavailable, err)
	}
	sort.Strings(identities)
	pipe := store.client.Pipeline()
	commands := make([]*redis.StringCmd, len(identities))
	for index, identity := range identities {
		commands[index] = pipe.Get(ctx, store.currentKeyFromIdentity(identity))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, store.backendError(ctx, "list metadata", orchestration.ErrSkillUnavailable, err)
	}
	metadata := make([]orchestration.SkillMetadata, 0, min(limit, len(commands)))
	for index, command := range commands {
		encoded, err := command.Bytes()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, store.backendError(ctx, "list metadata", orchestration.ErrSkillUnavailable, err)
		}
		var current storedSkillPublished
		if err := json.Unmarshal(encoded, &current); err != nil {
			return nil, store.backendError(ctx, "decode metadata", orchestration.ErrSkillIntegrity, err)
		}
		if current.Ref.Ref.String() != identities[index] {
			return nil, store.backendError(ctx, "verify metadata", orchestration.ErrSkillIntegrity, errors.New("catalog identity mismatch"))
		}
		if err := validateStoredSkillCurrent(current.Ref.Ref, current); err != nil {
			return nil, store.backendError(ctx, "verify metadata", orchestration.ErrSkillIntegrity, err)
		}
		item := current.Metadata
		if filter.Namespace != "" && item.Ref.Namespace != filter.Namespace ||
			filter.Domain != "" && !containsSkillString(item.Domains, filter.Domain) ||
			filter.Tag != "" && !containsSkillString(item.Tags, filter.Tag) {
			continue
		}
		metadata = append(metadata, item)
		if len(metadata) == limit {
			break
		}
	}
	return metadata, nil
}

func (store *SkillStore) ResolveCandidates(
	ctx context.Context,
	requests []orchestration.SkillCandidateRequest,
) ([]orchestration.SkillCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(requests) > maxSkillStoreBatchSize {
		return nil, fmt.Errorf("%w: candidate batch exceeds provider limit", orchestration.ErrSkillLimitExceeded)
	}
	if len(requests) == 0 {
		return []orchestration.SkillCandidate{}, nil
	}
	pipe := store.client.Pipeline()
	values := make([]*redis.StringCmd, len(requests))
	tombstones := make([]*redis.StringCmd, len(requests))
	versions := make([]uint64, len(requests))
	valid := make([]bool, len(requests))
	for index, request := range requests {
		requested := strings.TrimSpace(request.RequestedVersion)
		if requested == "" || requested == "published" {
			valid[index] = true
			values[index] = pipe.Get(ctx, store.currentKey(request.Ref))
			continue
		}
		version, err := strconv.ParseUint(requested, 10, 64)
		if err != nil || version == 0 {
			continue
		}
		valid[index] = true
		versions[index] = version
		values[index] = pipe.Get(ctx, store.candidateKey(request.Ref, version))
		tombstones[index] = pipe.Get(ctx, store.tombstoneKey(request.Ref, version))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, store.backendError(ctx, "resolve candidates", orchestration.ErrSkillUnavailable, err)
	}
	result := make([]orchestration.SkillCandidate, len(requests))
	for index, request := range requests {
		requested := strings.TrimSpace(request.RequestedVersion)
		if requested == "" {
			requested = "published"
		}
		candidate := orchestration.SkillCandidate{Ref: request.Ref, RequestedVersion: requested}
		if !valid[index] {
			candidate.Status = orchestration.SkillCandidateInvalidVersion
			result[index] = candidate
			continue
		}
		encoded, err := values[index].Bytes()
		if errors.Is(err, redis.Nil) {
			candidate.Status = orchestration.SkillCandidateNotFound
			if tombstones[index] != nil {
				if _, tombstoneErr := tombstones[index].Bytes(); tombstoneErr == nil {
					candidate.Status = orchestration.SkillCandidateDeleted
				} else if !errors.Is(tombstoneErr, redis.Nil) {
					return nil, store.backendError(ctx, "resolve tombstone", orchestration.ErrSkillUnavailable, tombstoneErr)
				}
			}
			result[index] = candidate
			continue
		}
		if err != nil {
			return nil, store.backendError(ctx, "resolve candidates", orchestration.ErrSkillUnavailable, err)
		}
		if versions[index] == 0 {
			var current storedSkillPublished
			if err := json.Unmarshal(encoded, &current); err != nil {
				return nil, store.backendError(ctx, "decode published candidate", orchestration.ErrSkillIntegrity, err)
			}
			if err := validateStoredSkillCurrent(request.Ref, current); err != nil {
				return nil, store.backendError(ctx, "verify published candidate", orchestration.ErrSkillIntegrity, err)
			}
			candidate.Resolved = current.Ref
			candidate.Metadata = current.Metadata
		} else {
			var exact storedSkillCandidate
			if err := json.Unmarshal(encoded, &exact); err != nil {
				return nil, store.backendError(ctx, "decode exact candidate", orchestration.ErrSkillIntegrity, err)
			}
			if err := validateStoredSkillCandidate(request.Ref, versions[index], exact); err != nil {
				return nil, store.backendError(ctx, "verify exact candidate", orchestration.ErrSkillIntegrity, err)
			}
			candidate.Resolved = exact.Ref
			candidate.Metadata = exact.Metadata
		}
		candidate.Status = orchestration.SkillCandidateResolved
		result[index] = candidate
	}
	return result, nil
}

func (store *SkillStore) GetManifest(ctx context.Context, ref orchestration.SkillVersionRef) (orchestration.SkillManifest, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillManifest{}, err
	}
	encoded, err := store.client.Get(ctx, store.manifestKey(ref.Ref, ref.Version)).Bytes()
	if errors.Is(err, redis.Nil) {
		return orchestration.SkillManifest{}, fmt.Errorf("%w: exact manifest unavailable", orchestration.ErrSkillRevisionNotFound)
	}
	if err != nil {
		return orchestration.SkillManifest{}, store.backendError(ctx, "load manifest", orchestration.ErrSkillUnavailable, err)
	}
	var manifest orchestration.SkillManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return orchestration.SkillManifest{}, store.backendError(ctx, "decode manifest", orchestration.ErrSkillIntegrity, err)
	}
	observed, err := orchestration.ComputeSkillManifestHash(manifest)
	if err != nil || manifest.Ref.Ref != ref.Ref || manifest.Ref.Version != ref.Version || observed != ref.ManifestHash {
		return orchestration.SkillManifest{}, fmt.Errorf("%w: exact manifest verification failed", orchestration.ErrSkillIntegrity)
	}
	manifest.Ref.ManifestHash = observed
	return manifest, nil
}

func (store *SkillStore) GetResource(ctx context.Context, ref orchestration.SkillResourceRef) (orchestration.SkillResource, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillResource{}, err
	}
	encoded, err := store.client.Get(ctx, store.resourceKey(ref.Skill.Ref, ref.Skill.Version, ref.Name)).Bytes()
	if errors.Is(err, redis.Nil) {
		return orchestration.SkillResource{}, fmt.Errorf("%w: exact resource unavailable", orchestration.ErrSkillRevisionNotFound)
	}
	if err != nil {
		return orchestration.SkillResource{}, store.backendError(ctx, "load resource", orchestration.ErrSkillUnavailable, err)
	}
	var resource orchestration.SkillResource
	if err := json.Unmarshal(encoded, &resource); err != nil {
		return orchestration.SkillResource{}, store.backendError(ctx, "decode resource", orchestration.ErrSkillIntegrity, err)
	}
	observed, err := orchestration.ComputeSkillResourceHash(resource)
	if err != nil || resource.Ref.Skill.Ref != ref.Skill.Ref || resource.Ref.Skill.Version != ref.Skill.Version ||
		resource.Ref.Name != ref.Name || observed != ref.ExpectedHash {
		return orchestration.SkillResource{}, fmt.Errorf("%w: exact resource verification failed", orchestration.ErrSkillIntegrity)
	}
	resource.Ref = ref
	return resource, nil
}

func (store *SkillStore) GetPublished(ctx context.Context, ref orchestration.SkillRef) (orchestration.SkillRevisionRepresentation, error) {
	current, err := store.loadCurrent(ctx, ref)
	if err != nil {
		return orchestration.SkillRevisionRepresentation{}, err
	}
	representation, err := store.loadRevision(ctx, ref, current.Ref.Version)
	if err != nil {
		return orchestration.SkillRevisionRepresentation{}, err
	}
	if representation.Revision.Ref != current.Ref ||
		representation.Revision.RevisionToken != current.RevisionToken ||
		!reflect.DeepEqual(representation.Revision.Metadata, current.Metadata) {
		return orchestration.SkillRevisionRepresentation{}, store.backendError(
			ctx, "verify published revision", orchestration.ErrSkillIntegrity,
			errors.New("published pointer and revision differ"),
		)
	}
	representation.Revision.RevisionToken = current.RevisionToken
	return representation, nil
}

func (store *SkillStore) GetVersion(ctx context.Context, ref orchestration.SkillRef, version uint64) (orchestration.SkillRevisionRepresentation, error) {
	if version == 0 {
		return orchestration.SkillRevisionRepresentation{}, fmt.Errorf("%w: version must be positive", orchestration.ErrInvalidSkillPackage)
	}
	return store.loadRevision(ctx, ref, version)
}

func (store *SkillStore) ListVersions(
	ctx context.Context,
	ref orchestration.SkillRef,
	options orchestration.SkillVersionListOptions,
) (orchestration.SkillVersionPage, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillVersionPage{}, err
	}
	limit := options.Limit
	if limit <= 0 {
		limit = defaultSkillListLimit
	}
	if limit > maxSkillListLimit {
		limit = maxSkillListLimit
	}
	maxVersion := "+inf"
	if options.BeforeVersion > 0 {
		maxVersion = "(" + strconv.FormatUint(options.BeforeVersion, 10)
	}
	versions, err := store.client.ZRevRangeByScore(ctx, store.versionsKey(ref), &redis.ZRangeBy{
		Max: maxVersion, Min: "-inf", Offset: 0, Count: int64(limit + 1),
	}).Result()
	if err != nil {
		return orchestration.SkillVersionPage{}, store.backendError(ctx, "list versions", orchestration.ErrSkillUnavailable, err)
	}
	page := orchestration.SkillVersionPage{Versions: make([]orchestration.SkillRevisionSummary, 0, min(limit, len(versions)))}
	if len(versions) > limit {
		next, parseErr := strconv.ParseUint(versions[limit-1], 10, 64)
		if parseErr != nil {
			return orchestration.SkillVersionPage{}, store.backendError(ctx, "decode version index", orchestration.ErrSkillIntegrity, parseErr)
		}
		page.NextBeforeVersion = next
		versions = versions[:limit]
	}
	pipe := store.client.Pipeline()
	revisions := make([]*redis.StringCmd, len(versions))
	tombstones := make([]*redis.StringCmd, len(versions))
	for index, raw := range versions {
		version, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil {
			return orchestration.SkillVersionPage{}, store.backendError(ctx, "decode version index", orchestration.ErrSkillIntegrity, parseErr)
		}
		revisions[index] = pipe.Get(ctx, store.revisionKey(ref, version))
		tombstones[index] = pipe.Get(ctx, store.tombstoneKey(ref, version))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return orchestration.SkillVersionPage{}, store.backendError(ctx, "list versions", orchestration.ErrSkillUnavailable, err)
	}
	for index := range versions {
		if encoded, err := revisions[index].Bytes(); err == nil {
			var revision storedSkillRevision
			if jsonErr := json.Unmarshal(encoded, &revision); jsonErr != nil {
				return orchestration.SkillVersionPage{}, store.backendError(ctx, "decode revision summary", orchestration.ErrSkillIntegrity, jsonErr)
			}
			version, _ := strconv.ParseUint(versions[index], 10, 64)
			if verifyErr := validateStoredSkillRevision(ref, version, revision); verifyErr != nil {
				return orchestration.SkillVersionPage{}, store.backendError(ctx, "verify revision summary", orchestration.ErrSkillIntegrity, verifyErr)
			}
			page.Versions = append(page.Versions, revision.Summary)
			continue
		} else if !errors.Is(err, redis.Nil) {
			return orchestration.SkillVersionPage{}, store.backendError(ctx, "list versions", orchestration.ErrSkillUnavailable, err)
		}
		encoded, err := tombstones[index].Bytes()
		if err != nil {
			return orchestration.SkillVersionPage{}, store.backendError(ctx, "load revision tombstone", orchestration.ErrSkillIntegrity, err)
		}
		var summary orchestration.SkillRevisionSummary
		if err := json.Unmarshal(encoded, &summary); err != nil {
			return orchestration.SkillVersionPage{}, store.backendError(ctx, "decode revision tombstone", orchestration.ErrSkillIntegrity, err)
		}
		version, _ := strconv.ParseUint(versions[index], 10, 64)
		if err := validateStoredSkillTombstone(ref, version, summary); err != nil {
			return orchestration.SkillVersionPage{}, store.backendError(ctx, "verify revision tombstone", orchestration.ErrSkillIntegrity, err)
		}
		page.Versions = append(page.Versions, summary)
	}
	return page, nil
}

func (store *SkillStore) PutPublished(
	ctx context.Context,
	input orchestration.PutPublishedSkillInput,
) (orchestration.PutPublishedSkillResult, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.PutPublishedSkillResult{}, err
	}
	if input.RequireAbsent && input.ExpectedRevisionToken != "" || !input.RequireAbsent && input.ExpectedRevisionToken == "" {
		return orchestration.PutPublishedSkillResult{}, fmt.Errorf("%w: exactly one publication precondition is required", orchestration.ErrSkillPrecondition)
	}
	var result orchestration.PutPublishedSkillResult
	keys := []string{store.currentKey(input.Ref), store.nextVersionKey(input.Ref)}
	if input.IdempotencyKey != "" {
		keys = append(keys, store.idempotencyKey(input.Ref, input.IdempotencyKey))
	}
	for attempt := 0; attempt < maxSkillStoreTxRetries; attempt++ {
		err := store.client.Watch(ctx, func(tx *redis.Tx) error {
			if input.IdempotencyKey != "" {
				encoded, err := tx.Get(ctx, store.idempotencyKey(input.Ref, input.IdempotencyKey)).Bytes()
				if err == nil {
					var prior storedSkillIdempotency
					if jsonErr := json.Unmarshal(encoded, &prior); jsonErr != nil {
						return store.backendError(ctx, "decode idempotency record", orchestration.ErrSkillIntegrity, jsonErr)
					}
					if validationErr := validateStoredSkillIdempotency(input.Ref, prior); validationErr != nil {
						return store.backendError(ctx, "verify idempotency record", orchestration.ErrSkillIntegrity, validationErr)
					}
					if !orchestration.SkillVersionedAuthoringContentEqual(prior.Package, input.Package) {
						return fmt.Errorf("%w: idempotency key was used for different content", orchestration.ErrSkillConflict)
					}
					result = prior.Result
					result.Current.RevisionToken = prior.RevisionToken
					result.Outcome = orchestration.SkillAuditIdempotentReplay
					return nil
				}
				if !errors.Is(err, redis.Nil) {
					return store.backendError(ctx, "read idempotency record", orchestration.ErrSkillUnavailable, err)
				}
			}

			current, currentErr := loadStoredCurrent(ctx, tx, store.currentKey(input.Ref))
			if currentErr != nil && !errors.Is(currentErr, redis.Nil) {
				return store.backendError(ctx, "read publication state", orchestration.ErrSkillUnavailable, currentErr)
			}
			if currentErr == nil {
				if err := validateStoredSkillCurrent(input.Ref, current); err != nil {
					return store.backendError(ctx, "verify publication state", orchestration.ErrSkillIntegrity, err)
				}
			}
			if input.RequireAbsent {
				if currentErr == nil {
					return fmt.Errorf("%w: skill already exists", orchestration.ErrSkillPrecondition)
				}
			} else if currentErr != nil || current.RevisionToken != input.ExpectedRevisionToken {
				return fmt.Errorf("%w: publication precondition failed", orchestration.ErrSkillPrecondition)
			}

			if currentErr == nil {
				prior, err := loadStoredRevision(ctx, tx, store.revisionKey(input.Ref, current.Ref.Version))
				if err != nil {
					return store.backendError(ctx, "read current revision", orchestration.ErrSkillIntegrity, err)
				}
				if err := validateStoredSkillRevision(input.Ref, current.Ref.Version, prior); err != nil {
					return store.backendError(ctx, "verify current revision", orchestration.ErrSkillIntegrity, err)
				}
				if orchestration.SkillVersionedAuthoringContentEqual(
					orchestration.ValidatedSkillPackage{Package: prior.Representation.Package}, input.Package,
				) {
					result = orchestration.PutPublishedSkillResult{
						Outcome: orchestration.SkillAuditSameContentNoOp,
						Current: publishedFromStored(current),
					}
					return store.persistSkillIdempotency(ctx, tx, input, result)
				}
			}

			next, err := tx.Get(ctx, store.nextVersionKey(input.Ref)).Uint64()
			if errors.Is(err, redis.Nil) {
				next = 0
			} else if err != nil {
				return store.backendError(ctx, "read next revision", orchestration.ErrSkillUnavailable, err)
			}
			if next == math.MaxUint64 {
				return fmt.Errorf("%w: skill version space is exhausted", orchestration.ErrSkillLimitExceeded)
			}
			version := next + 1
			revision, resources, err := buildStoredSkillRevision(input.Ref, version, input.Package)
			if err != nil {
				return err
			}
			token := "skills-v1-" + uuid.NewString()
			revision.Representation.Revision.RevisionToken = token
			revision.RevisionToken = token
			published := storedSkillPublished{
				Ref:           revision.Representation.Revision.Ref,
				Metadata:      revision.Representation.Revision.Metadata,
				RevisionToken: token,
			}
			result = orchestration.PutPublishedSkillResult{
				Outcome: orchestration.SkillAuditCreated,
				Current: publishedFromStored(published),
			}
			if currentErr == nil {
				previous := current.Ref
				result.Previous = &previous
				result.Outcome = orchestration.SkillAuditUpdated
			}
			encodedCurrent, _ := json.Marshal(published)
			encodedCandidate, _ := json.Marshal(storedSkillCandidate{
				Ref: revision.Representation.Revision.Ref, Metadata: revision.Representation.Revision.Metadata,
			})
			encodedRevision, _ := json.Marshal(revision)
			encodedManifest, _ := json.Marshal(revision.Representation.Manifest)
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, store.currentKey(input.Ref), encodedCurrent, 0)
				pipe.Set(ctx, store.nextVersionKey(input.Ref), version, 0)
				pipe.Set(ctx, store.candidateKey(input.Ref, version), encodedCandidate, 0)
				pipe.Set(ctx, store.revisionKey(input.Ref, version), encodedRevision, 0)
				pipe.Set(ctx, store.manifestKey(input.Ref, version), encodedManifest, 0)
				for _, resource := range resources {
					encodedResource, _ := json.Marshal(resource)
					pipe.Set(ctx, store.resourceKey(input.Ref, version, resource.Ref.Name), encodedResource, 0)
				}
				pipe.ZAdd(ctx, store.versionsKey(input.Ref), &redis.Z{Score: float64(version), Member: strconv.FormatUint(version, 10)})
				pipe.SAdd(ctx, store.catalogKey(), input.Ref.String())
				if input.IdempotencyKey != "" {
					encodedIdempotency, _ := json.Marshal(storedSkillIdempotency{
						Package: input.Package, Result: result, RevisionToken: result.Current.RevisionToken,
					})
					pipe.Set(ctx, store.idempotencyKey(input.Ref, input.IdempotencyKey), encodedIdempotency, 0)
				}
				return nil
			})
			return err
		}, keys...)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return orchestration.PutPublishedSkillResult{}, store.backendErrorUnlessDomain(ctx, "publish", err)
		}
		return result, nil
	}
	return orchestration.PutPublishedSkillResult{}, fmt.Errorf("%w: concurrent publication retry limit reached", orchestration.ErrSkillConflict)
}

func (store *SkillStore) DeleteVersions(
	ctx context.Context,
	input orchestration.DeleteSkillVersionsInput,
) (orchestration.DeleteSkillVersionsResult, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.DeleteSkillVersionsResult{}, err
	}
	if input.FromVersion == 0 || input.ToVersion < input.FromVersion || input.ExpectedRevisionToken == "" || strings.TrimSpace(input.Reason) == "" {
		return orchestration.DeleteSkillVersionsResult{}, fmt.Errorf("%w: invalid deletion command", orchestration.ErrInvalidSkillPackage)
	}
	// Compare the zero-based delta before adding one so an inclusive range
	// ending at math.MaxUint64 cannot wrap around the provider guard.
	if input.ToVersion-input.FromVersion >= uint64(maxSkillStoreBatchSize) {
		return orchestration.DeleteSkillVersionsResult{}, fmt.Errorf("%w: deletion range exceeds provider limit", orchestration.ErrSkillLimitExceeded)
	}
	versionCount := input.ToVersion - input.FromVersion + 1
	keys := []string{store.currentKey(input.Ref)}
	for offset := uint64(0); offset < versionCount; offset++ {
		version := input.FromVersion + offset
		keys = append(keys, store.revisionKey(input.Ref, version), store.tombstoneKey(input.Ref, version))
	}
	var result orchestration.DeleteSkillVersionsResult
	for attempt := 0; attempt < maxSkillStoreTxRetries; attempt++ {
		err := store.client.Watch(ctx, func(tx *redis.Tx) error {
			current, err := loadStoredCurrent(ctx, tx, store.currentKey(input.Ref))
			if errors.Is(err, redis.Nil) {
				return fmt.Errorf("%w: skill does not exist", orchestration.ErrSkillNotFound)
			}
			if err != nil {
				return store.backendError(ctx, "read deletion state", orchestration.ErrSkillUnavailable, err)
			}
			if err := validateStoredSkillCurrent(input.Ref, current); err != nil {
				return store.backendError(ctx, "verify deletion state", orchestration.ErrSkillIntegrity, err)
			}
			if current.RevisionToken != input.ExpectedRevisionToken {
				return fmt.Errorf("%w: deletion precondition failed", orchestration.ErrSkillPrecondition)
			}
			protectedPrevious := uint64(0)
			if current.Ref.Version > 1 {
				protectedPrevious = current.Ref.Version - 1
			}
			if input.FromVersion <= current.Ref.Version && current.Ref.Version <= input.ToVersion ||
				protectedPrevious > 0 && input.FromVersion <= protectedPrevious && protectedPrevious <= input.ToVersion {
				return fmt.Errorf("%w: deletion intersects protected revisions", orchestration.ErrSkillProtectedRevision)
			}
			result = orchestration.DeleteSkillVersionsResult{
				Outcome: orchestration.SkillAuditDeleted, Ref: input.Ref,
				PreviousPublished: current.Ref, CurrentPublished: current.Ref,
				DeletedVersions: make([]uint64, 0), AlreadyDeletedVersions: make([]uint64, 0),
			}
			revisions := make(map[uint64]storedSkillRevision)
			for offset := uint64(0); offset < versionCount; offset++ {
				version := input.FromVersion + offset
				revision, revisionErr := loadStoredRevision(ctx, tx, store.revisionKey(input.Ref, version))
				if revisionErr == nil {
					if err := validateStoredSkillRevision(input.Ref, version, revision); err != nil {
						return store.backendError(ctx, "verify deletion target", orchestration.ErrSkillIntegrity, err)
					}
					revisions[version] = revision
					result.DeletedVersions = append(result.DeletedVersions, version)
					continue
				}
				if !errors.Is(revisionErr, redis.Nil) {
					return store.backendError(ctx, "read deletion target", orchestration.ErrSkillUnavailable, revisionErr)
				}
				result.AlreadyDeletedVersions = append(result.AlreadyDeletedVersions, version)
			}
			if len(result.DeletedVersions) == 0 {
				result.Outcome = orchestration.SkillAuditDeleteNoOp
				return nil
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				for version, revision := range revisions {
					summary := revision.Summary
					now := store.now().UTC()
					summary.Status = orchestration.SkillRevisionDeleted
					summary.DeletedAt = &now
					summary.Reason = strings.TrimSpace(input.Reason)
					summary.Actor = strings.TrimSpace(input.Actor)
					encoded, _ := json.Marshal(summary)
					pipe.Set(ctx, store.tombstoneKey(input.Ref, version), encoded, 0)
					pipe.Del(
						ctx,
						store.candidateKey(input.Ref, version),
						store.revisionKey(input.Ref, version),
						store.manifestKey(input.Ref, version),
					)
					for _, resource := range revision.Representation.Manifest.Resources {
						pipe.Del(ctx, store.resourceKey(input.Ref, version, resource.Name))
					}
				}
				return nil
			})
			return err
		}, keys...)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return orchestration.DeleteSkillVersionsResult{}, store.backendErrorUnlessDomain(ctx, "delete versions", err)
		}
		return result, nil
	}
	return orchestration.DeleteSkillVersionsResult{}, fmt.Errorf("%w: concurrent deletion retry limit reached", orchestration.ErrSkillConflict)
}

func (store *SkillStore) RecordSkillAudit(ctx context.Context, event orchestration.SkillAuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.RequestID) == "" || event.OccurredAt.IsZero() ||
		event.Ref.Namespace == "" || event.Ref.Name == "" || strings.TrimSpace(event.Reason) == "" {
		return fmt.Errorf("%w: audit event is incomplete", orchestration.ErrInvalidSkillPackage)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("%w: encode audit event", orchestration.ErrInvalidSkillPackage)
	}
	key := store.auditKey(event.EventID)
	return store.backendErrorUnlessDomain(ctx, "record audit", store.client.Watch(ctx, func(tx *redis.Tx) error {
		prior, err := tx.Get(ctx, key).Bytes()
		if err == nil {
			if string(prior) != string(encoded) {
				return fmt.Errorf("%w: audit event identifier conflict", orchestration.ErrSkillConflict)
			}
			return nil
		}
		if !errors.Is(err, redis.Nil) {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, encoded, 0)
			pipe.ZAdd(ctx, store.auditIndexKey(), &redis.Z{Score: float64(event.OccurredAt.UnixMilli()), Member: event.EventID})
			return nil
		})
		return err
	}, key))
}

func buildStoredSkillRevision(
	ref orchestration.SkillRef,
	version uint64,
	validated orchestration.ValidatedSkillPackage,
) (storedSkillRevision, []orchestration.SkillResource, error) {
	input := validated.Package
	versionRef := orchestration.SkillVersionRef{Ref: ref, Version: version}
	resources := make([]orchestration.SkillResource, len(input.Resources))
	resourceMetadata := make([]orchestration.SkillResourceMetadata, len(input.Resources))
	for index, authored := range input.Resources {
		resource := orchestration.SkillResource{
			Ref:         orchestration.SkillResourceRef{Skill: versionRef, Name: authored.Name},
			ContentType: authored.ContentType, Content: authored.Content,
		}
		hash, err := orchestration.ComputeSkillResourceHash(resource)
		if err != nil {
			return storedSkillRevision{}, nil, err
		}
		resource.Ref.ExpectedHash = hash
		resources[index] = resource
		resourceMetadata[index] = orchestration.SkillResourceMetadata{
			Name: authored.Name, Description: authored.Description, LoadWhen: authored.LoadWhen,
			AppliesTo:            append([]orchestration.SkillResourceScope(nil), authored.AppliesTo...),
			RequiredWhenSelected: authored.RequiredWhenSelected,
			ContentType:          authored.ContentType, ResourceHash: hash,
		}
	}
	manifest := orchestration.SkillManifest{
		Ref: versionRef, DisplayName: input.DisplayName, Description: input.Description,
		Domains: append([]string(nil), input.Domains...), Tags: append([]string(nil), input.Tags...),
		PlanningInstructions: append([]string(nil), input.PlanningInstructions...),
		ResponseInstructions: append([]string(nil), input.ResponseInstructions...),
		ToolHints:            append([]string(nil), input.ToolHints...), Resources: resourceMetadata,
	}
	manifestHash, err := orchestration.ComputeSkillManifestHash(manifest)
	if err != nil {
		return storedSkillRevision{}, nil, err
	}
	manifest.Ref.ManifestHash = manifestHash
	for index := range resources {
		resources[index].Ref.Skill.ManifestHash = manifestHash
	}
	metadata := orchestration.SkillMetadata{
		Ref: ref, DisplayName: input.DisplayName, Description: input.Description,
		Domains: append([]string(nil), input.Domains...), Tags: append([]string(nil), input.Tags...),
		PublishedVersion: version, Status: orchestration.SkillPublicationPublished,
	}
	published := orchestration.PublishedSkillRevision{Ref: manifest.Ref, Metadata: metadata}
	return storedSkillRevision{
		Representation: orchestration.SkillRevisionRepresentation{Revision: published, Package: input, Manifest: manifest},
		Summary:        orchestration.SkillRevisionSummary{Ref: manifest.Ref, Status: orchestration.SkillRevisionRetained},
	}, resources, nil
}

func (store *SkillStore) persistSkillIdempotency(
	ctx context.Context,
	tx *redis.Tx,
	input orchestration.PutPublishedSkillInput,
	result orchestration.PutPublishedSkillResult,
) error {
	if input.IdempotencyKey == "" {
		return nil
	}
	encoded, err := json.Marshal(storedSkillIdempotency{
		Package: input.Package, Result: result, RevisionToken: result.Current.RevisionToken,
	})
	if err != nil {
		return err
	}
	_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, store.idempotencyKey(input.Ref, input.IdempotencyKey), encoded, 0)
		return nil
	})
	return err
}

func (store *SkillStore) loadCurrent(ctx context.Context, ref orchestration.SkillRef) (storedSkillPublished, error) {
	current, err := loadStoredCurrent(ctx, store.client, store.currentKey(ref))
	if errors.Is(err, redis.Nil) {
		return storedSkillPublished{}, fmt.Errorf("%w: skill does not exist", orchestration.ErrSkillNotFound)
	}
	if err != nil {
		if isSkillStoredJSONError(err) {
			return storedSkillPublished{}, store.backendError(ctx, "decode published revision", orchestration.ErrSkillIntegrity, err)
		}
		return storedSkillPublished{}, store.backendError(ctx, "load published revision", orchestration.ErrSkillUnavailable, err)
	}
	if err := validateStoredSkillCurrent(ref, current); err != nil {
		return storedSkillPublished{}, store.backendError(ctx, "verify published revision", orchestration.ErrSkillIntegrity, err)
	}
	return current, nil
}

func (store *SkillStore) loadRevision(ctx context.Context, ref orchestration.SkillRef, version uint64) (orchestration.SkillRevisionRepresentation, error) {
	revision, err := loadStoredRevision(ctx, store.client, store.revisionKey(ref, version))
	if errors.Is(err, redis.Nil) {
		return orchestration.SkillRevisionRepresentation{}, fmt.Errorf("%w: revision does not exist", orchestration.ErrSkillRevisionNotFound)
	}
	if err != nil {
		if isSkillStoredJSONError(err) {
			return orchestration.SkillRevisionRepresentation{}, store.backendError(ctx, "decode revision", orchestration.ErrSkillIntegrity, err)
		}
		return orchestration.SkillRevisionRepresentation{}, store.backendError(ctx, "load revision", orchestration.ErrSkillUnavailable, err)
	}
	if err := validateStoredSkillRevision(ref, version, revision); err != nil {
		return orchestration.SkillRevisionRepresentation{}, store.backendError(ctx, "verify revision", orchestration.ErrSkillIntegrity, err)
	}
	revision.Representation.Revision.RevisionToken = revision.RevisionToken
	return revision.Representation, nil
}

func validateStoredSkillCurrent(ref orchestration.SkillRef, current storedSkillPublished) error {
	if current.Ref.Ref != ref || current.Ref.Version == 0 || !validStoredSkillHash(current.Ref.ManifestHash) ||
		current.Metadata.Ref != ref || current.Metadata.PublishedVersion != current.Ref.Version ||
		current.Metadata.Status != orchestration.SkillPublicationPublished ||
		strings.TrimSpace(current.RevisionToken) == "" {
		return errors.New("published skill record is inconsistent")
	}
	return nil
}

func validateStoredSkillCandidate(
	ref orchestration.SkillRef,
	version uint64,
	candidate storedSkillCandidate,
) error {
	if version == 0 || candidate.Ref.Ref != ref || candidate.Ref.Version != version ||
		!validStoredSkillHash(candidate.Ref.ManifestHash) || candidate.Metadata.Ref != ref ||
		candidate.Metadata.PublishedVersion != version ||
		candidate.Metadata.Status != orchestration.SkillPublicationPublished {
		return errors.New("exact skill candidate record is inconsistent")
	}
	return nil
}

func validateStoredSkillRevision(
	ref orchestration.SkillRef,
	version uint64,
	revision storedSkillRevision,
) error {
	if version == 0 || strings.TrimSpace(revision.RevisionToken) == "" {
		return errors.New("skill revision identity is incomplete")
	}
	expected, _, err := buildStoredSkillRevision(
		ref,
		version,
		orchestration.ValidatedSkillPackage{Package: revision.Representation.Package},
	)
	if err != nil {
		return errors.New("skill revision content is invalid")
	}
	expected.RevisionToken = revision.RevisionToken
	if !reflect.DeepEqual(revision, expected) {
		return errors.New("skill revision representation is inconsistent")
	}
	return nil
}

func validateStoredSkillTombstone(
	ref orchestration.SkillRef,
	version uint64,
	summary orchestration.SkillRevisionSummary,
) error {
	if version == 0 || summary.Ref.Ref != ref || summary.Ref.Version != version ||
		!validStoredSkillHash(summary.Ref.ManifestHash) || summary.Status != orchestration.SkillRevisionDeleted ||
		summary.DeletedAt == nil || strings.TrimSpace(summary.Reason) == "" {
		return errors.New("skill revision tombstone is inconsistent")
	}
	return nil
}

func validateStoredSkillIdempotency(
	ref orchestration.SkillRef,
	record storedSkillIdempotency,
) error {
	current := record.Result.Current
	if strings.TrimSpace(record.RevisionToken) == "" || current.Ref.Ref != ref ||
		current.Ref.Version == 0 || !validStoredSkillHash(current.Ref.ManifestHash) ||
		current.Metadata.Ref != ref || current.Metadata.PublishedVersion != current.Ref.Version ||
		current.Metadata.Status != orchestration.SkillPublicationPublished {
		return errors.New("skill idempotency record is inconsistent")
	}
	expected, _, err := buildStoredSkillRevision(ref, current.Ref.Version, record.Package)
	if err != nil || expected.Representation.Revision.Ref != current.Ref ||
		!reflect.DeepEqual(expected.Representation.Revision.Metadata, current.Metadata) {
		return errors.New("skill idempotency content is inconsistent")
	}
	switch record.Result.Outcome {
	case orchestration.SkillAuditCreated, orchestration.SkillAuditSameContentNoOp:
		if record.Result.Previous != nil {
			return errors.New("skill idempotency outcome is inconsistent")
		}
	case orchestration.SkillAuditUpdated:
		previous := record.Result.Previous
		if previous == nil || previous.Ref != ref || previous.Version == 0 ||
			previous.Version >= current.Ref.Version || !validStoredSkillHash(previous.ManifestHash) {
			return errors.New("skill idempotency outcome is inconsistent")
		}
	default:
		return errors.New("skill idempotency outcome is invalid")
	}
	return nil
}

func validStoredSkillHash(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func isSkillStoredJSONError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}

type stringGetter interface {
	Get(context.Context, string) *redis.StringCmd
}

func loadStoredCurrent(ctx context.Context, client stringGetter, key string) (storedSkillPublished, error) {
	encoded, err := client.Get(ctx, key).Bytes()
	if err != nil {
		return storedSkillPublished{}, err
	}
	var current storedSkillPublished
	if err := json.Unmarshal(encoded, &current); err != nil {
		return storedSkillPublished{}, err
	}
	return current, nil
}

func loadStoredRevision(ctx context.Context, client stringGetter, key string) (storedSkillRevision, error) {
	encoded, err := client.Get(ctx, key).Bytes()
	if err != nil {
		return storedSkillRevision{}, err
	}
	var revision storedSkillRevision
	if err := json.Unmarshal(encoded, &revision); err != nil {
		return storedSkillRevision{}, err
	}
	return revision, nil
}

func publishedFromStored(value storedSkillPublished) orchestration.PublishedSkillRevision {
	return orchestration.PublishedSkillRevision{Ref: value.Ref, Metadata: value.Metadata, RevisionToken: value.RevisionToken}
}

func containsSkillString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (store *SkillStore) backendError(ctx context.Context, operation string, category error, cause error) error {
	store.logFailure(ctx, operation, cause)
	return fmt.Errorf("%w: %s: %w", category, operation, core.RedactSensitiveError(cause))
}

func (store *SkillStore) backendErrorUnlessDomain(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	for _, category := range []error{
		orchestration.ErrInvalidSkillPackage, orchestration.ErrSkillNotFound,
		orchestration.ErrSkillRevisionNotFound, orchestration.ErrSkillIntegrity,
		orchestration.ErrSkillUnavailable, orchestration.ErrSkillLimitExceeded,
		orchestration.ErrSkillConflict, orchestration.ErrSkillPrecondition,
		orchestration.ErrSkillProtectedRevision,
	} {
		if errors.Is(err, category) {
			return err
		}
	}
	return store.backendError(ctx, operation, orchestration.ErrSkillUnavailable, err)
}

func (store *SkillStore) logFailure(ctx context.Context, operation string, err error) {
	if store.logger == nil || err == nil {
		return
	}
	store.logger.WarnWithContext(ctx, "Skill store operation failed", map[string]interface{}{
		"operation": "skill_store", "request_id": orchestration.GetRequestID(ctx),
		"status": "failed", "reason": "backend_operation_failed",
		"error_type": operation, "error": core.RedactSensitiveText(err.Error()),
	})
}

func nilSkillStoreLogger(logger core.Logger) bool {
	if logger == nil {
		return true
	}
	value := reflect.ValueOf(logger)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (store *SkillStore) storagePrefix() string { return store.keyPrefix + ":{store}" }
func (store *SkillStore) catalogKey() string    { return store.storagePrefix() + ":catalog" }
func (store *SkillStore) currentKey(ref orchestration.SkillRef) string {
	return store.currentKeyFromIdentity(ref.String())
}
func (store *SkillStore) currentKeyFromIdentity(identity string) string {
	return store.storagePrefix() + ":skill:" + identity + ":current"
}
func (store *SkillStore) nextVersionKey(ref orchestration.SkillRef) string {
	return store.storagePrefix() + ":skill:" + ref.String() + ":next"
}
func (store *SkillStore) versionsKey(ref orchestration.SkillRef) string {
	return store.storagePrefix() + ":skill:" + ref.String() + ":versions"
}
func (store *SkillStore) revisionKey(ref orchestration.SkillRef, version uint64) string {
	return store.storagePrefix() + ":skill:" + ref.String() + ":revision:" + strconv.FormatUint(version, 10)
}
func (store *SkillStore) candidateKey(ref orchestration.SkillRef, version uint64) string {
	return store.storagePrefix() + ":skill:" + ref.String() + ":candidate:" + strconv.FormatUint(version, 10)
}
func (store *SkillStore) manifestKey(ref orchestration.SkillRef, version uint64) string {
	return store.storagePrefix() + ":skill:" + ref.String() + ":manifest:" + strconv.FormatUint(version, 10)
}
func (store *SkillStore) resourceKey(ref orchestration.SkillRef, version uint64, name string) string {
	return store.storagePrefix() + ":skill:" + ref.String() + ":resource:" + strconv.FormatUint(version, 10) + ":" + name
}
func (store *SkillStore) tombstoneKey(ref orchestration.SkillRef, version uint64) string {
	return store.storagePrefix() + ":skill:" + ref.String() + ":tombstone:" + strconv.FormatUint(version, 10)
}
func (store *SkillStore) idempotencyKey(ref orchestration.SkillRef, value string) string {
	digest := sha256.Sum256([]byte(value))
	return store.storagePrefix() + ":skill:" + ref.String() + ":idempotency:" + hex.EncodeToString(digest[:])
}
func (store *SkillStore) auditKey(eventID string) string {
	digest := sha256.Sum256([]byte(eventID))
	return store.storagePrefix() + ":audit:" + hex.EncodeToString(digest[:])
}
func (store *SkillStore) auditIndexKey() string { return store.storagePrefix() + ":audits" }

var (
	_ orchestration.SkillRegistry              = (*SkillStore)(nil)
	_ orchestration.SkillRevisionReader        = (*SkillStore)(nil)
	_ orchestration.SkillAdministrationStore   = (*SkillStore)(nil)
	_ orchestration.SkillRevisionDeletionStore = (*SkillStore)(nil)
	_ orchestration.SkillAuditSink             = (*SkillStore)(nil)
)
