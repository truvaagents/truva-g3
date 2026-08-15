package backendconformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

func TestSkillConformanceSemanticReference(t *testing.T) {
	RunSkillConformance(t, func(t *testing.T) SkillFixture {
		t.Helper()
		store := newSemanticSkillStore()
		return SkillFixture{
			Registry: store, PeerRegistry: store, Revisions: store, PeerRevisions: store,
			Administration: store, Deletions: store,
			CorruptManifest: store.corruptManifest,
			CorruptResource: store.corruptResource,
		}
	})
}

type semanticSkillStore struct {
	mu     sync.RWMutex
	skills map[orchestration.SkillRef]*semanticSkillRecord
}

type semanticSkillRecord struct {
	published  uint64
	next       uint64
	revisions  map[uint64]*semanticSkillRevision
	idempotent map[string]semanticSkillIdempotency
}

type semanticSkillRevision struct {
	representation orchestration.SkillRevisionRepresentation
	resources      map[string]orchestration.SkillResource
	deleted        bool
	deletedAt      time.Time
	deleteReason   string
	deleteActor    string
}

type semanticSkillIdempotency struct {
	packageInput orchestration.SkillPackageInput
	result       orchestration.PutPublishedSkillResult
}

func newSemanticSkillStore() *semanticSkillStore {
	return &semanticSkillStore{skills: make(map[orchestration.SkillRef]*semanticSkillRecord)}
}

func (store *semanticSkillStore) ListMetadata(
	ctx context.Context,
	filter orchestration.SkillMetadataFilter,
) ([]orchestration.SkillMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]orchestration.SkillMetadata, 0, len(store.skills))
	for _, record := range store.skills {
		revision := record.revisions[record.published]
		metadata := revision.representation.Revision.Metadata
		if filter.Namespace != "" && metadata.Ref.Namespace != filter.Namespace ||
			filter.Domain != "" && !containsSemanticString(metadata.Domains, filter.Domain) ||
			filter.Tag != "" && !containsSemanticString(metadata.Tags, filter.Tag) {
			continue
		}
		result = append(result, cloneSemanticJSON(metadata))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref.String() < result[j].Ref.String() })
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (store *semanticSkillStore) ResolveCandidates(
	ctx context.Context,
	requests []orchestration.SkillCandidateRequest,
) ([]orchestration.SkillCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]orchestration.SkillCandidate, 0, len(requests))
	for _, request := range requests {
		candidate := orchestration.SkillCandidate{
			Ref: request.Ref, RequestedVersion: request.RequestedVersion,
		}
		record := store.skills[request.Ref]
		if record == nil {
			candidate.Status = orchestration.SkillCandidateNotFound
			result = append(result, candidate)
			continue
		}
		version, valid := semanticRequestedVersion(request.RequestedVersion, record.published)
		if !valid {
			candidate.Status = orchestration.SkillCandidateInvalidVersion
			result = append(result, candidate)
			continue
		}
		revision := record.revisions[version]
		if revision == nil {
			candidate.Status = orchestration.SkillCandidateNotFound
		} else if revision.deleted {
			candidate.Status = orchestration.SkillCandidateDeleted
		} else {
			candidate.Status = orchestration.SkillCandidateResolved
			candidate.Resolved = revision.representation.Revision.Ref
			candidate.Metadata = cloneSemanticJSON(revision.representation.Revision.Metadata)
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (store *semanticSkillStore) GetManifest(
	ctx context.Context,
	ref orchestration.SkillVersionRef,
) (orchestration.SkillManifest, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillManifest{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	revision, err := store.exactRevision(ref.Ref, ref.Version)
	if err != nil {
		return orchestration.SkillManifest{}, err
	}
	manifest := cloneSemanticJSON(revision.representation.Manifest)
	if ref.ManifestHash != "" && ref.ManifestHash != manifest.Ref.ManifestHash {
		return orchestration.SkillManifest{}, fmt.Errorf("%w: semantic manifest reference mismatch", orchestration.ErrSkillIntegrity)
	}
	observed, err := orchestration.ComputeSkillManifestHash(manifest)
	if err != nil || observed != manifest.Ref.ManifestHash {
		return orchestration.SkillManifest{}, fmt.Errorf("%w: semantic manifest verification failed", orchestration.ErrSkillIntegrity)
	}
	return manifest, nil
}

func (store *semanticSkillStore) GetResource(
	ctx context.Context,
	ref orchestration.SkillResourceRef,
) (orchestration.SkillResource, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillResource{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	revision, err := store.exactRevision(ref.Skill.Ref, ref.Skill.Version)
	if err != nil {
		return orchestration.SkillResource{}, err
	}
	resource, found := revision.resources[ref.Name]
	if !found {
		return orchestration.SkillResource{}, fmt.Errorf("%w: semantic resource not found", orchestration.ErrSkillRevisionNotFound)
	}
	if ref.Skill.ManifestHash != "" && ref.Skill.ManifestHash != resource.Ref.Skill.ManifestHash ||
		ref.ExpectedHash != resource.Ref.ExpectedHash {
		return orchestration.SkillResource{}, fmt.Errorf("%w: semantic resource reference mismatch", orchestration.ErrSkillIntegrity)
	}
	observed, err := orchestration.ComputeSkillResourceHash(resource)
	if err != nil || observed != resource.Ref.ExpectedHash {
		return orchestration.SkillResource{}, fmt.Errorf("%w: semantic resource verification failed", orchestration.ErrSkillIntegrity)
	}
	return cloneSemanticJSON(resource), nil
}

func (store *semanticSkillStore) GetPublished(
	ctx context.Context,
	ref orchestration.SkillRef,
) (orchestration.SkillRevisionRepresentation, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillRevisionRepresentation{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record := store.skills[ref]
	if record == nil {
		return orchestration.SkillRevisionRepresentation{}, fmt.Errorf("%w: semantic skill not found", orchestration.ErrSkillNotFound)
	}
	return cloneSemanticRepresentation(record.revisions[record.published].representation), nil
}

func (store *semanticSkillStore) GetVersion(
	ctx context.Context,
	ref orchestration.SkillRef,
	version uint64,
) (orchestration.SkillRevisionRepresentation, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillRevisionRepresentation{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	revision, err := store.exactRevision(ref, version)
	if err != nil {
		return orchestration.SkillRevisionRepresentation{}, err
	}
	return cloneSemanticRepresentation(revision.representation), nil
}

func (store *semanticSkillStore) ListVersions(
	ctx context.Context,
	ref orchestration.SkillRef,
	options orchestration.SkillVersionListOptions,
) (orchestration.SkillVersionPage, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.SkillVersionPage{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record := store.skills[ref]
	if record == nil {
		return orchestration.SkillVersionPage{}, fmt.Errorf("%w: semantic skill not found", orchestration.ErrSkillNotFound)
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	versions := make([]uint64, 0, len(record.revisions))
	for version := range record.revisions {
		if options.BeforeVersion == 0 || version < options.BeforeVersion {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	page := orchestration.SkillVersionPage{Versions: make([]orchestration.SkillRevisionSummary, 0, min(limit, len(versions)))}
	for index, version := range versions {
		if index == limit {
			page.NextBeforeVersion = versions[index-1]
			break
		}
		revision := record.revisions[version]
		summary := orchestration.SkillRevisionSummary{
			Ref: revision.representation.Revision.Ref, Status: orchestration.SkillRevisionRetained,
		}
		if revision.deleted {
			deletedAt := revision.deletedAt
			summary.Status = orchestration.SkillRevisionDeleted
			summary.DeletedAt = &deletedAt
			summary.Reason = revision.deleteReason
			summary.Actor = revision.deleteActor
		}
		page.Versions = append(page.Versions, summary)
	}
	return page, nil
}

func (store *semanticSkillStore) PutPublished(
	ctx context.Context,
	input orchestration.PutPublishedSkillInput,
) (orchestration.PutPublishedSkillResult, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.PutPublishedSkillResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record := store.skills[input.Ref]
	newRecord := record == nil
	if record != nil && input.IdempotencyKey != "" {
		if prior, found := record.idempotent[input.IdempotencyKey]; found {
			if !reflect.DeepEqual(prior.packageInput, input.Package.Package) {
				return orchestration.PutPublishedSkillResult{}, fmt.Errorf("%w: semantic idempotency conflict", orchestration.ErrSkillConflict)
			}
			result := cloneSemanticPutResult(prior.result)
			result.Outcome = orchestration.SkillAuditIdempotentReplay
			return result, nil
		}
	}
	if newRecord {
		if !input.RequireAbsent || input.ExpectedRevisionToken != "" {
			return orchestration.PutPublishedSkillResult{}, fmt.Errorf("%w: semantic create precondition", orchestration.ErrSkillConflict)
		}
		record = &semanticSkillRecord{
			revisions:  make(map[uint64]*semanticSkillRevision),
			idempotent: make(map[string]semanticSkillIdempotency),
		}
	} else {
		current := record.revisions[record.published].representation.Revision
		if input.RequireAbsent || input.ExpectedRevisionToken == "" || input.ExpectedRevisionToken != current.RevisionToken {
			return orchestration.PutPublishedSkillResult{}, fmt.Errorf("%w: semantic update precondition", orchestration.ErrSkillConflict)
		}
		if orchestration.SkillVersionedAuthoringContentEqual(
			input.Package,
			orchestration.ValidatedSkillPackage{Package: record.revisions[record.published].representation.Package},
		) {
			result := orchestration.PutPublishedSkillResult{
				Outcome: orchestration.SkillAuditSameContentNoOp,
				Current: cloneSemanticPublishedRevision(current),
			}
			previous := current.Ref
			result.Previous = &previous
			store.rememberIdempotent(input, result, record)
			return result, nil
		}
	}

	previousVersion := record.published
	version := record.next + 1
	semanticRevision, err := buildSemanticSkillRevision(input.Ref, version, input.Package.Package)
	if err != nil {
		return orchestration.PutPublishedSkillResult{}, err
	}
	record.next = version
	record.revisions[version] = semanticRevision
	record.published = version
	if newRecord {
		store.skills[input.Ref] = record
	}
	result := orchestration.PutPublishedSkillResult{
		Outcome: orchestration.SkillAuditCreated,
		Current: cloneSemanticPublishedRevision(semanticRevision.representation.Revision),
	}
	if previousVersion > 0 {
		result.Outcome = orchestration.SkillAuditUpdated
		previous := record.revisions[previousVersion].representation.Revision.Ref
		result.Previous = &previous
	}
	store.rememberIdempotent(input, result, record)
	return result, nil
}

func (store *semanticSkillStore) DeleteVersions(
	ctx context.Context,
	input orchestration.DeleteSkillVersionsInput,
) (orchestration.DeleteSkillVersionsResult, error) {
	if err := ctx.Err(); err != nil {
		return orchestration.DeleteSkillVersionsResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record := store.skills[input.Ref]
	if record == nil {
		return orchestration.DeleteSkillVersionsResult{}, fmt.Errorf("%w: semantic skill not found", orchestration.ErrSkillNotFound)
	}
	current := record.revisions[record.published].representation.Revision
	if input.ExpectedRevisionToken == "" || input.ExpectedRevisionToken != current.RevisionToken ||
		input.FromVersion == 0 || input.ToVersion < input.FromVersion {
		return orchestration.DeleteSkillVersionsResult{}, fmt.Errorf("%w: semantic deletion precondition", orchestration.ErrSkillPrecondition)
	}
	protectedPrevious := uint64(0)
	if record.published > 1 {
		protectedPrevious = record.published - 1
	}
	for version := input.FromVersion; version <= input.ToVersion; version++ {
		if version == record.published || version == protectedPrevious {
			return orchestration.DeleteSkillVersionsResult{}, fmt.Errorf("%w: semantic protected revision", orchestration.ErrSkillProtectedRevision)
		}
		if record.revisions[version] == nil {
			return orchestration.DeleteSkillVersionsResult{}, fmt.Errorf("%w: semantic revision not found", orchestration.ErrSkillRevisionNotFound)
		}
	}
	result := orchestration.DeleteSkillVersionsResult{
		Outcome: orchestration.SkillAuditDeleteNoOp, Ref: input.Ref,
		PreviousPublished: current.Ref, CurrentPublished: current.Ref,
	}
	for version := input.FromVersion; version <= input.ToVersion; version++ {
		revision := record.revisions[version]
		if revision.deleted {
			result.AlreadyDeletedVersions = append(result.AlreadyDeletedVersions, version)
			continue
		}
		revision.deleted = true
		revision.deletedAt = time.Now().UTC()
		revision.deleteReason = input.Reason
		revision.deleteActor = input.Actor
		revision.resources = nil
		revision.representation.Package = orchestration.SkillPackageInput{}
		revision.representation.Manifest = orchestration.SkillManifest{}
		result.DeletedVersions = append(result.DeletedVersions, version)
	}
	if len(result.DeletedVersions) > 0 {
		result.Outcome = orchestration.SkillAuditDeleted
	}
	return result, nil
}

func (store *semanticSkillStore) exactRevision(
	ref orchestration.SkillRef,
	version uint64,
) (*semanticSkillRevision, error) {
	record := store.skills[ref]
	if record == nil || record.revisions[version] == nil || record.revisions[version].deleted {
		return nil, fmt.Errorf("%w: semantic revision not found", orchestration.ErrSkillRevisionNotFound)
	}
	return record.revisions[version], nil
}

func (store *semanticSkillStore) rememberIdempotent(
	input orchestration.PutPublishedSkillInput,
	result orchestration.PutPublishedSkillResult,
	record *semanticSkillRecord,
) {
	if input.IdempotencyKey == "" {
		return
	}
	record.idempotent[input.IdempotencyKey] = semanticSkillIdempotency{
		packageInput: cloneSemanticJSON(input.Package.Package),
		result:       cloneSemanticPutResult(result),
	}
}

func (store *semanticSkillStore) corruptManifest(ref orchestration.SkillVersionRef) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	revision, err := store.exactRevision(ref.Ref, ref.Version)
	if err != nil {
		return err
	}
	revision.representation.Manifest.PlanningInstructions[0] += " corrupted"
	return nil
}

func (store *semanticSkillStore) corruptResource(ref orchestration.SkillResourceRef) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	revision, err := store.exactRevision(ref.Skill.Ref, ref.Skill.Version)
	if err != nil {
		return err
	}
	resource, found := revision.resources[ref.Name]
	if !found {
		return errors.New("semantic resource missing")
	}
	resource.Content += " corrupted"
	revision.resources[ref.Name] = resource
	return nil
}

func buildSemanticSkillRevision(
	ref orchestration.SkillRef,
	version uint64,
	input orchestration.SkillPackageInput,
) (*semanticSkillRevision, error) {
	versionRef := orchestration.SkillVersionRef{Ref: ref, Version: version}
	resourceMetadata := make([]orchestration.SkillResourceMetadata, 0, len(input.Resources))
	resources := make(map[string]orchestration.SkillResource, len(input.Resources))
	for _, authored := range input.Resources {
		resource := orchestration.SkillResource{
			Ref:         orchestration.SkillResourceRef{Skill: versionRef, Name: authored.Name},
			ContentType: authored.ContentType, Content: authored.Content,
		}
		hash, err := orchestration.ComputeSkillResourceHash(resource)
		if err != nil {
			return nil, err
		}
		resource.Ref.ExpectedHash = hash
		resources[authored.Name] = resource
		resourceMetadata = append(resourceMetadata, orchestration.SkillResourceMetadata{
			Name: authored.Name, Description: authored.Description, LoadWhen: authored.LoadWhen,
			AppliesTo:            append([]orchestration.SkillResourceScope(nil), authored.AppliesTo...),
			RequiredWhenSelected: authored.RequiredWhenSelected,
			ContentType:          authored.ContentType, ResourceHash: hash,
		})
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
		return nil, err
	}
	manifest.Ref.ManifestHash = manifestHash
	for name, resource := range resources {
		resource.Ref.Skill.ManifestHash = manifestHash
		resources[name] = resource
	}
	metadata := orchestration.SkillMetadata{
		Ref: ref, DisplayName: input.DisplayName, Description: input.Description,
		Domains: append([]string(nil), input.Domains...), Tags: append([]string(nil), input.Tags...),
		PublishedVersion: version, Status: orchestration.SkillPublicationPublished,
	}
	published := orchestration.PublishedSkillRevision{
		Ref: manifest.Ref, Metadata: metadata, RevisionToken: fmt.Sprintf("semantic-etag-%d", version),
	}
	return &semanticSkillRevision{
		representation: orchestration.SkillRevisionRepresentation{
			Revision: published, Package: cloneSemanticJSON(input), Manifest: manifest,
		},
		resources: resources,
	}, nil
}

func semanticRequestedVersion(value string, published uint64) (uint64, bool) {
	if value == "" || value == "published" {
		return published, true
	}
	version, err := strconv.ParseUint(value, 10, 64)
	return version, err == nil && version > 0
}

func containsSemanticString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneSemanticRepresentation(value orchestration.SkillRevisionRepresentation) orchestration.SkillRevisionRepresentation {
	cloned := cloneSemanticJSON(value)
	cloned.Revision.RevisionToken = value.Revision.RevisionToken
	return cloned
}

func cloneSemanticPublishedRevision(value orchestration.PublishedSkillRevision) orchestration.PublishedSkillRevision {
	cloned := cloneSemanticJSON(value)
	cloned.RevisionToken = value.RevisionToken
	return cloned
}

func cloneSemanticPutResult(value orchestration.PutPublishedSkillResult) orchestration.PutPublishedSkillResult {
	cloned := cloneSemanticJSON(value)
	cloned.Current.RevisionToken = value.Current.RevisionToken
	return cloned
}

func cloneSemanticJSON[T any](value T) T {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var cloned T
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		panic(err)
	}
	return cloned
}

var (
	_ orchestration.SkillRegistry              = (*semanticSkillStore)(nil)
	_ orchestration.SkillRevisionReader        = (*semanticSkillStore)(nil)
	_ orchestration.SkillAdministrationStore   = (*semanticSkillStore)(nil)
	_ orchestration.SkillRevisionDeletionStore = (*semanticSkillStore)(nil)
)
