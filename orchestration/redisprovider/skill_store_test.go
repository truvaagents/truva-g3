package redisprovider

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/backendconformance"
)

func TestRedisSkillStoreConformance(t *testing.T) {
	backendconformance.RunSkillConformance(t, func(t *testing.T) backendconformance.SkillFixture {
		server := miniredis.RunT(t)
		firstClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
		secondClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() {
			_ = firstClient.Close()
			_ = secondClient.Close()
		})
		first, err := NewSkillStore(firstClient, WithSkillStoreKeyPrefix("conformance:skills"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := NewSkillStore(secondClient, WithSkillStoreKeyPrefix("conformance:skills"))
		if err != nil {
			t.Fatal(err)
		}
		return backendconformance.SkillFixture{
			Registry: first, PeerRegistry: second, Revisions: first, PeerRevisions: second,
			Administration: first, Deletions: first,
			CorruptManifest: func(ref orchestration.SkillVersionRef) error {
				encoded, err := firstClient.Get(t.Context(), first.manifestKey(ref.Ref, ref.Version)).Bytes()
				if err != nil {
					return err
				}
				var manifest orchestration.SkillManifest
				if err := json.Unmarshal(encoded, &manifest); err != nil {
					return err
				}
				manifest.PlanningInstructions[0] += " corrupted-sensitive-body-marker"
				encoded, err = json.Marshal(manifest)
				if err != nil {
					return err
				}
				return firstClient.Set(t.Context(), first.manifestKey(ref.Ref, ref.Version), encoded, 0).Err()
			},
			CorruptResource: func(ref orchestration.SkillResourceRef) error {
				encoded, err := firstClient.Get(t.Context(), first.resourceKey(ref.Skill.Ref, ref.Skill.Version, ref.Name)).Bytes()
				if err != nil {
					return err
				}
				var resource orchestration.SkillResource
				if err := json.Unmarshal(encoded, &resource); err != nil {
					return err
				}
				resource.Content += " corrupted-sensitive-body-marker"
				encoded, err = json.Marshal(resource)
				if err != nil {
					return err
				}
				return firstClient.Set(t.Context(), first.resourceKey(ref.Skill.Ref, ref.Skill.Version, ref.Name), encoded, 0).Err()
			},
		}
	})
}

func TestRedisSkillStoreAuditIsIdempotentAndBodyFree(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	event := orchestration.SkillAuditEvent{
		EventID: "event-1", RequestID: "request-1", OccurredAt: time.Unix(100, 0).UTC(),
		Action: orchestration.SkillAuditPutPublished, Outcome: orchestration.SkillAuditCreated,
		Ref: orchestration.SkillRef{Namespace: "travel", Name: "weather"}, Reason: "initial publish",
	}
	if err := store.RecordSkillAudit(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSkillAudit(t.Context(), event); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	changed := event
	changed.Reason = "different"
	if err := store.RecordSkillAudit(t.Context(), changed); !errors.Is(err, orchestration.ErrSkillConflict) {
		t.Fatalf("conflicting replay = %v", err)
	}
	encoded, err := client.Get(t.Context(), store.auditKey(event.EventID)).Result()
	if err != nil || strings.Contains(encoded, "planning_instructions") || strings.Contains(encoded, "resource_content") {
		t.Fatalf("stored audit = %q, %v", encoded, err)
	}
}

func TestRedisSkillStoreRoundTripsPublishedSkillWithoutResources(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	ref := orchestration.SkillRef{Namespace: "travel", Name: "action-verification"}
	publishRedisSkill(t, store, ref, "travel", "verification")

	if _, err := store.GetPublished(t.Context(), ref); err != nil {
		t.Fatalf("GetPublished() after JSON round trip error = %v", err)
	}
	page, err := store.ListVersions(t.Context(), ref, orchestration.SkillVersionListOptions{})
	if err != nil {
		t.Fatalf("ListVersions() after JSON round trip error = %v", err)
	}
	if len(page.Versions) != 1 || page.Versions[0].Ref.Version != 1 {
		t.Fatalf("ListVersions() = %#v, want retained version 1", page)
	}
}

func TestRedisSkillStoreDeleteSingleMaxUint64VersionTerminates(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	ref := orchestration.SkillRef{Namespace: "limits", Name: "max-version"}
	publishRedisSkill(t, store, ref, "testing", "limits")
	current, err := loadStoredCurrent(t.Context(), client, store.currentKey(ref))
	if err != nil {
		t.Fatalf("loadStoredCurrent() error = %v", err)
	}

	result, err := store.DeleteVersions(t.Context(), orchestration.DeleteSkillVersionsInput{
		Ref: ref, FromVersion: math.MaxUint64, ToVersion: math.MaxUint64,
		ExpectedRevisionToken: current.RevisionToken,
		Reason:                "verify bounded max-version deletion",
	})
	if err != nil {
		t.Fatalf("DeleteVersions(MaxUint64) error = %v", err)
	}
	if result.Outcome != orchestration.SkillAuditDeleteNoOp ||
		len(result.AlreadyDeletedVersions) != 1 || result.AlreadyDeletedVersions[0] != math.MaxUint64 {
		t.Fatalf("DeleteVersions(MaxUint64) = %#v", result)
	}
}

func TestRedisSkillStoreRejectsRangeWidthOverflow(t *testing.T) {
	store := &SkillStore{}
	_, err := store.DeleteVersions(t.Context(), orchestration.DeleteSkillVersionsInput{
		Ref:                   orchestration.SkillRef{Namespace: "limits", Name: "overflow"},
		FromVersion:           1,
		ToVersion:             math.MaxUint64,
		ExpectedRevisionToken: "revision-token",
		Reason:                "verify overflow-safe range limit",
	})
	if !errors.Is(err, orchestration.ErrSkillLimitExceeded) {
		t.Fatalf("DeleteVersions(overflowing range) error = %v, want ErrSkillLimitExceeded", err)
	}
}

func TestRedisSkillPresetAndClientRoleComposition(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clients, err := NewClientSet(nil, WithRoleClient(ClientRoleSkills, client))
	if err != nil {
		t.Fatal(err)
	}
	options, err := NewOptions(WithNamespace("tenant"))
	if err != nil {
		t.Fatal(err)
	}
	backends, err := NewOrchestrationBackends(clients, options)
	if err != nil {
		t.Fatal(err)
	}
	if backends.SkillRegistry() == nil || backends.SkillRevisionReader() == nil ||
		backends.SkillAdministrationStore() == nil || backends.SkillRevisionDeletionStore() == nil ||
		backends.SkillAuditSink() == nil {
		t.Fatalf("skill capabilities were not composed: %#v", backends)
	}
	store, ok := backends.SkillRegistry().(*SkillStore)
	if !ok || store.keyPrefix != "tenant:skills" {
		t.Fatalf("skill store = %#v", backends.SkillRegistry())
	}
}

type skillPipelineCountingHook struct {
	mu               sync.Mutex
	pipelines        int
	commands         int
	pipelineCommands []string
}

func (hook *skillPipelineCountingHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (hook *skillPipelineCountingHook) AfterProcess(context.Context, redis.Cmder) error {
	hook.mu.Lock()
	hook.commands++
	hook.mu.Unlock()
	return nil
}
func (hook *skillPipelineCountingHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}
func (hook *skillPipelineCountingHook) AfterProcessPipeline(_ context.Context, commands []redis.Cmder) error {
	hook.mu.Lock()
	hook.pipelines++
	for _, command := range commands {
		hook.pipelineCommands = append(hook.pipelineCommands, command.String())
	}
	hook.mu.Unlock()
	return nil
}

func TestRedisSkillCandidateResolutionUsesOnePipeline(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	requests := []orchestration.SkillCandidateRequest{
		{Ref: orchestration.SkillRef{Namespace: "travel", Name: "weather"}, RequestedVersion: "published"},
		{Ref: orchestration.SkillRef{Namespace: "devops", Name: "incident"}, RequestedVersion: "7"},
	}
	hook := &skillPipelineCountingHook{}
	client.AddHook(hook)
	if _, err := store.ResolveCandidates(t.Context(), requests); err != nil {
		t.Fatal(err)
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	if hook.pipelines != 1 || hook.commands != 0 {
		t.Fatalf("pipeline calls = %d, direct commands = %d; want 1 and 0", hook.pipelines, hook.commands)
	}
	for _, command := range hook.pipelineCommands {
		if strings.Contains(command, ":revision:") || strings.Contains(command, ":manifest:") ||
			strings.Contains(command, ":resource:") {
			t.Fatalf("candidate resolution loaded a body-bearing key: %q", command)
		}
	}
}

func TestRedisSkillCandidateResolutionRejectsOversizedBatch(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]orchestration.SkillCandidateRequest, maxSkillStoreBatchSize+1)
	for index := range requests {
		requests[index] = orchestration.SkillCandidateRequest{
			Ref: orchestration.SkillRef{Namespace: "batch", Name: "skill"}, RequestedVersion: "published",
		}
	}
	if _, err := store.ResolveCandidates(t.Context(), requests); !errors.Is(err, orchestration.ErrSkillLimitExceeded) {
		t.Fatalf("ResolveCandidates(oversized) error = %v", err)
	}
}

func TestRedisSkillStoreRejectsMalformedCurrentAndHistoryRecords(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logger := &skillStoreCaptureLogger{}
	store, err := NewSkillStore(client, WithSkillStoreLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	ref := orchestration.SkillRef{Namespace: "malformed", Name: "records"}
	publishRedisSkill(t, store, ref, "testing", "malformed")
	if err := client.Set(t.Context(), store.currentKey(ref), "{not-json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveCandidates(t.Context(), []orchestration.SkillCandidateRequest{{
		Ref: ref, RequestedVersion: "published",
	}}); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("ResolveCandidates(malformed current) error = %v", err)
	}
	if len(logger.warnings) != 1 || logger.warnings[0]["error_type"] != "unmarshal" ||
		logger.warnings[0]["store_operation"] != "decode_published_candidate" {
		t.Fatalf("malformed current warnings = %#v", logger.warnings)
	}
	if err := client.Set(t.Context(), store.revisionKey(ref, 1), "{not-json", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetVersion(t.Context(), ref, 1); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("GetVersion(malformed history) error = %v", err)
	}
	if len(logger.warnings) != 2 || logger.warnings[1]["error_type"] != "unmarshal" ||
		logger.warnings[1]["store_operation"] != "decode_revision" {
		t.Fatalf("malformed history warnings = %#v", logger.warnings)
	}
}

func TestRedisSkillStoreRejectsSemanticallyInconsistentRecords(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logger := &skillStoreCaptureLogger{}
	store, err := NewSkillStore(client, WithSkillStoreLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	ref := orchestration.SkillRef{Namespace: "integrity", Name: "records"}
	publishRedisSkill(t, store, ref, "testing", "integrity")

	currentBytes, err := client.Get(t.Context(), store.currentKey(ref)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var current storedSkillPublished
	if err := json.Unmarshal(currentBytes, &current); err != nil {
		t.Fatal(err)
	}
	current.Metadata.PublishedVersion++
	corruptedCurrent, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.currentKey(ref), corruptedCurrent, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveCandidates(t.Context(), []orchestration.SkillCandidateRequest{{
		Ref: ref, RequestedVersion: "published",
	}}); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("ResolveCandidates(inconsistent current) error = %v", err)
	}
	if len(logger.warnings) != 1 || logger.warnings[0]["error_type"] != "integrity" ||
		logger.warnings[0]["store_operation"] != "verify_published_candidate" {
		t.Fatalf("inconsistent current warnings = %#v", logger.warnings)
	}
	if _, err := store.ListMetadata(t.Context(), orchestration.SkillMetadataFilter{}); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("ListMetadata(inconsistent current) error = %v", err)
	}
	if err := client.Set(t.Context(), store.currentKey(ref), currentBytes, 0).Err(); err != nil {
		t.Fatal(err)
	}

	revisionBytes, err := client.Get(t.Context(), store.revisionKey(ref, 1)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var revision storedSkillRevision
	if err := json.Unmarshal(revisionBytes, &revision); err != nil {
		t.Fatal(err)
	}
	revision.Representation.Manifest.Description = "corrupted but valid JSON"
	corruptedRevision, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.revisionKey(ref, 1), corruptedRevision, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetVersion(t.Context(), ref, 1); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("GetVersion(inconsistent revision) error = %v", err)
	}
	if _, err := store.ListVersions(t.Context(), ref, orchestration.SkillVersionListOptions{}); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("ListVersions(inconsistent revision) error = %v", err)
	}

	candidateBytes, err := client.Get(t.Context(), store.candidateKey(ref, 1)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var candidate storedSkillCandidate
	if err := json.Unmarshal(candidateBytes, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Metadata.PublishedVersion++
	corruptedCandidate, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), store.candidateKey(ref, 1), corruptedCandidate, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveCandidates(t.Context(), []orchestration.SkillCandidateRequest{{
		Ref: ref, RequestedVersion: "1",
	}}); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("ResolveCandidates(inconsistent exact candidate) error = %v", err)
	}
}

func TestRedisSkillStoreRejectsSemanticallyInconsistentIdempotencyRecord(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	ref := orchestration.SkillRef{Namespace: "integrity", Name: "idempotency"}
	validated := redisValidatedSkill(t, ref, "testing", "integrity")
	input := orchestration.PutPublishedSkillInput{
		Ref: ref, Package: validated, RequireAbsent: true, IdempotencyKey: "publish-once",
	}
	if _, err := store.PutPublished(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	key := store.idempotencyKey(ref, input.IdempotencyKey)
	encoded, err := client.Get(t.Context(), key).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var record storedSkillIdempotency
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	record.Result.Current.Metadata.PublishedVersion++
	encoded, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), key, encoded, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutPublished(t.Context(), input); !errors.Is(err, orchestration.ErrSkillIntegrity) {
		t.Fatalf("PutPublished(corrupt idempotency) error = %v, want ErrSkillIntegrity", err)
	}
}

func TestRedisSkillStoreFailedPublicationLeavesNoPartialRevision(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logger := &skillStoreCaptureLogger{}
	store, err := NewSkillStore(client, WithSkillStoreLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	ref := orchestration.SkillRef{Namespace: "atomic", Name: "publication"}
	validated := redisValidatedSkill(t, ref, "atomic", "publication")
	server.SetError("ERR injected write failure")
	_, err = store.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
		Ref: ref, Package: validated, RequireAbsent: true,
	})
	server.SetError("")
	if !errors.Is(err, orchestration.ErrSkillUnavailable) {
		t.Fatalf("PutPublished(injected failure) error = %v", err)
	}
	if len(logger.warnings) != 1 || logger.warnings[0]["error_type"] != "store_write" ||
		logger.warnings[0]["store_operation"] != "publish" {
		t.Fatalf("failed publication warnings = %#v", logger.warnings)
	}
	for _, key := range []string{
		store.currentKey(ref), store.candidateKey(ref, 1), store.revisionKey(ref, 1), store.manifestKey(ref, 1),
	} {
		if client.Exists(t.Context(), key).Val() != 0 {
			t.Fatalf("failed publication left key %q", key)
		}
	}
}

func TestRedisSkillStoreRejectsExhaustedVersionSpace(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	ref := orchestration.SkillRef{Namespace: "version", Name: "exhausted"}
	if err := client.Set(t.Context(), store.nextVersionKey(ref), uint64(math.MaxUint64), 0).Err(); err != nil {
		t.Fatal(err)
	}
	_, err = store.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
		Ref: ref, Package: redisValidatedSkill(t, ref, "testing", "version"), RequireAbsent: true,
	})
	if !errors.Is(err, orchestration.ErrSkillLimitExceeded) {
		t.Fatalf("PutPublished(exhausted) error = %v", err)
	}
	if client.Exists(t.Context(), store.currentKey(ref)).Val() != 0 {
		t.Fatal("exhausted publication wrote current state")
	}
}

func TestRedisSkillStoreListsFilteredBoundedMetadata(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	publishRedisSkill(t, store, orchestration.SkillRef{Namespace: "travel", Name: "weather"}, "travel", "weather")
	publishRedisSkill(t, store, orchestration.SkillRef{Namespace: "devops", Name: "incident"}, "devops", "incident")

	metadata, err := store.ListMetadata(t.Context(), orchestration.SkillMetadataFilter{
		Namespace: "travel", Domain: "travel", Tag: "weather", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 1 || metadata[0].Ref != (orchestration.SkillRef{Namespace: "travel", Name: "weather"}) ||
		metadata[0].PublishedVersion != 1 {
		t.Fatalf("filtered metadata = %#v", metadata)
	}
	metadata, err = store.ListMetadata(t.Context(), orchestration.SkillMetadataFilter{Namespace: "missing"})
	if err != nil || len(metadata) != 0 {
		t.Fatalf("missing namespace metadata = %#v, %v", metadata, err)
	}
}

type skillStoreCaptureLogger struct {
	core.NoOpLogger
	warnings []map[string]interface{}
}

func TestWithSkillStoreLoggerRejectsTypedNil(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	var logger *skillStoreCaptureLogger
	if _, err := NewSkillStore(client, WithSkillStoreLogger(logger)); err == nil {
		t.Fatal("NewSkillStore() accepted a typed-nil logger")
	}
}

func (logger *skillStoreCaptureLogger) WarnWithContext(
	_ context.Context,
	_ string,
	fields map[string]interface{},
) {
	logger.warnings = append(logger.warnings, fields)
}

func TestRedisSkillStoreBackendFailureIsClassifiedAndLogged(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logger := &skillStoreCaptureLogger{}
	store, err := NewSkillStore(client, WithSkillStoreLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	ctx := orchestration.WithRequestID(t.Context(), "request-skill-store")
	_, err = store.ListMetadata(ctx, orchestration.SkillMetadataFilter{})
	if !errors.Is(err, orchestration.ErrSkillUnavailable) {
		t.Fatalf("ListMetadata() error = %v", err)
	}
	if len(logger.warnings) != 1 || logger.warnings[0]["error_type"] != "store_read" ||
		logger.warnings[0]["store_operation"] != "list_metadata" ||
		logger.warnings[0]["error"] == "" || logger.warnings[0]["request_id"] != "request-skill-store" ||
		logger.warnings[0]["status"] != "failed" || logger.warnings[0]["reason"] != "backend_operation_failed" {
		t.Fatalf("warnings = %#v", logger.warnings)
	}
}

func publishRedisSkill(
	t *testing.T,
	store *SkillStore,
	ref orchestration.SkillRef,
	domain string,
	tag string,
) {
	t.Helper()
	validated := redisValidatedSkill(t, ref, domain, tag)
	if _, err := store.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
		Ref: ref, Package: validated, RequireAbsent: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func redisValidatedSkill(
	t *testing.T,
	ref orchestration.SkillRef,
	domain string,
	tag string,
) orchestration.ValidatedSkillPackage {
	t.Helper()
	validator, err := orchestration.NewDefaultSkillPackageValidator(
		orchestration.DefaultSkillAuthoringLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}
	validated, _, err := validator.Validate(t.Context(), ref, orchestration.SkillPackageInput{
		DisplayName: ref.Name + " guidance",
		Description: "Use when a request needs " + ref.Name + " guidance.",
		Domains:     []string{domain},
		Tags:        []string{tag},
		PlanningInstructions: []string{
			"Follow the published " + ref.Name + " procedure.",
		},
		ResponseInstructions: []string{"Report the outcome and uncertainty."},
		ChangeReason:         "Initial test publication",
	})
	if err != nil {
		t.Fatal(err)
	}
	return validated
}
