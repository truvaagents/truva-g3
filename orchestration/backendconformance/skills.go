package backendconformance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

// SkillFixture contains independently injected views over one provider-backed
// skill store. Corruption callbacks are test-only adapter hooks used to prove
// integrity classification without making mutation part of the public API.
type SkillFixture struct {
	Registry        orchestration.SkillRegistry
	PeerRegistry    orchestration.SkillRegistry
	Revisions       orchestration.SkillRevisionReader
	PeerRevisions   orchestration.SkillRevisionReader
	Administration  orchestration.SkillAdministrationStore
	Deletions       orchestration.SkillRevisionDeletionStore
	CorruptManifest func(orchestration.SkillVersionRef) error
	CorruptResource func(orchestration.SkillResourceRef) error
}

// SkillFactory constructs a fresh isolated provider fixture for each
// conformance subtest.
type SkillFactory func(*testing.T) SkillFixture

// RunSkillConformance verifies the provider-neutral immutable publication,
// runtime-read, history, deletion, cancellation, and integrity contracts.
func RunSkillConformance(t *testing.T, factory SkillFactory) {
	t.Helper()

	t.Run("publication and exact immutable reads", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		ref := conformanceSkillRef("publication")
		first := publishConformanceSkill(t, fixture, ref, conformanceSkillPackage("initial"), true, "", "create-1")
		if first.Outcome != orchestration.SkillAuditCreated || first.Current.Ref.Version != 1 {
			t.Fatalf("first publication = %#v", first)
		}
		assertCandidateResolution(t, fixture.PeerRegistry, []orchestration.SkillCandidateRequest{{
			Ref: ref, RequestedVersion: "published",
		}}, map[string]orchestration.SkillVersionRef{ref.String() + "@published": first.Current.Ref})

		manifest, err := fixture.Registry.GetManifest(t.Context(), first.Current.Ref)
		if err != nil {
			t.Fatalf("GetManifest() error = %v", err)
		}
		if manifest.Ref != first.Current.Ref || len(manifest.Resources) != 1 {
			t.Fatalf("manifest = %#v", manifest)
		}
		resourceRef := orchestration.SkillResourceRef{
			Skill: first.Current.Ref, Name: manifest.Resources[0].Name,
			ExpectedHash: manifest.Resources[0].ResourceHash,
		}
		resource, err := fixture.Registry.GetResource(t.Context(), resourceRef)
		if err != nil || resource.Ref != resourceRef || resource.Content == "" {
			t.Fatalf("resource = %#v, %v", resource, err)
		}

		secondPackage := conformanceSkillPackage("second")
		secondPackage.Package.PlanningInstructions[0] += " Then classify disruption severity."
		second := publishConformanceSkill(t, fixture, ref, secondPackage, false, first.Current.RevisionToken, "update-2")
		if second.Outcome != orchestration.SkillAuditUpdated || second.Current.Ref.Version != 2 {
			t.Fatalf("second publication = %#v", second)
		}
		old, err := fixture.Registry.GetManifest(t.Context(), first.Current.Ref)
		if err != nil || old.Ref != first.Current.Ref || old.PlanningInstructions[0] != manifest.PlanningInstructions[0] {
			t.Fatalf("old immutable manifest = %#v, %v", old, err)
		}
		published, err := fixture.PeerRevisions.GetPublished(t.Context(), ref)
		if err != nil || published.Revision.Ref != second.Current.Ref {
			t.Fatalf("GetPublished() = %#v, %v", published, err)
		}
	})

	t.Run("batch resolution is complete keyed and stable", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		firstRef := conformanceSkillRef("batch-first")
		secondRef := conformanceSkillRef("batch-second")
		first := publishConformanceSkill(t, fixture, firstRef, conformanceSkillPackage("first"), true, "", "batch-1")
		second := publishConformanceSkill(t, fixture, secondRef, conformanceSkillPackage("second"), true, "", "batch-2")
		requests := []orchestration.SkillCandidateRequest{
			{Ref: secondRef, RequestedVersion: "published"},
			{Ref: firstRef, RequestedVersion: "1"},
			{Ref: conformanceSkillRef("missing"), RequestedVersion: "published"},
			{Ref: firstRef, RequestedVersion: "not-a-version"},
		}
		got, err := fixture.Registry.ResolveCandidates(t.Context(), requests)
		if err != nil {
			t.Fatalf("ResolveCandidates() error = %v", err)
		}
		if len(got) != len(requests) {
			t.Fatalf("ResolveCandidates() returned %d values, want %d", len(got), len(requests))
		}
		keyed := keySkillCandidates(got)
		assertResolvedCandidate(t, keyed[secondRef.String()+"@published"], second.Current.Ref)
		assertResolvedCandidate(t, keyed[firstRef.String()+"@1"], first.Current.Ref)
		if keyed[conformanceSkillRef("missing").String()+"@published"].Status != orchestration.SkillCandidateNotFound {
			t.Fatalf("missing candidate = %#v", keyed)
		}
		if keyed[firstRef.String()+"@not-a-version"].Status != orchestration.SkillCandidateInvalidVersion {
			t.Fatalf("invalid-version candidate = %#v", keyed)
		}

		again, err := fixture.Registry.ResolveCandidates(t.Context(), requests)
		if err != nil || !sameCandidateIdentitySet(got, again) {
			t.Fatalf("repeated candidate resolution = %#v, %v; first = %#v", again, err, got)
		}
	})

	t.Run("same content and idempotent publication", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		ref := conformanceSkillRef("idempotence")
		validated := conformanceSkillPackage("initial")
		first := publishConformanceSkill(t, fixture, ref, validated, true, "", "same-key")

		replay, err := fixture.Administration.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
			Ref: ref, Package: validated, RequireAbsent: true, IdempotencyKey: "same-key",
		})
		if err != nil || replay.Outcome != orchestration.SkillAuditIdempotentReplay || replay.Current.Ref != first.Current.Ref {
			t.Fatalf("idempotent replay = %#v, %v", replay, err)
		}

		changed := conformanceSkillPackage("changed")
		changed.Package.PlanningInstructions[0] += " Changed."
		_, err = fixture.Administration.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
			Ref: ref, Package: changed, ExpectedRevisionToken: first.Current.RevisionToken,
			IdempotencyKey: "same-key",
		})
		if !errors.Is(err, orchestration.ErrSkillConflict) {
			t.Fatalf("idempotency-key reuse error = %v, want ErrSkillConflict", err)
		}

		same := orchestration.ValidatedSkillPackage{Package: cloneConformanceSkillPackage(validated.Package)}
		same.Package.ChangeReason = "different audit reason"
		noOp := publishConformanceSkill(t, fixture, ref, same, false, first.Current.RevisionToken, "no-op-key")
		if noOp.Outcome != orchestration.SkillAuditSameContentNoOp || noOp.Current.Ref != first.Current.Ref {
			t.Fatalf("same-content publication = %#v", noOp)
		}
	})

	t.Run("concurrent publication assigns monotonic revisions", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		ref := conformanceSkillRef("concurrent")
		first := publishConformanceSkill(t, fixture, ref, conformanceSkillPackage("initial"), true, "", "concurrent-initial")

		const publishers = 8
		var wait sync.WaitGroup
		versions := make(chan uint64, publishers)
		errorsSeen := make(chan error, publishers)
		for index := 0; index < publishers; index++ {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				for attempt := 0; attempt < 128; attempt++ {
					current, err := fixture.Revisions.GetPublished(t.Context(), ref)
					if err != nil {
						errorsSeen <- err
						return
					}
					validated := conformanceSkillPackage(fmt.Sprintf("concurrent-%d", index))
					validated.Package.PlanningInstructions[0] += fmt.Sprintf(" Publisher %d.", index)
					result, err := fixture.Administration.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
						Ref: ref, Package: validated, ExpectedRevisionToken: current.Revision.RevisionToken,
						IdempotencyKey: fmt.Sprintf("concurrent-%d", index),
					})
					if errors.Is(err, orchestration.ErrSkillConflict) {
						continue
					}
					if err != nil {
						errorsSeen <- err
						return
					}
					versions <- result.Current.Ref.Version
					return
				}
				errorsSeen <- errors.New("concurrent publication exceeded conflict retry bound")
			}(index)
		}
		wait.Wait()
		close(versions)
		close(errorsSeen)
		for err := range errorsSeen {
			t.Fatalf("concurrent PutPublished() error = %v", err)
		}
		got := make([]uint64, 0, publishers)
		for version := range versions {
			got = append(got, version)
		}
		if len(got) != publishers {
			t.Fatalf("successful publisher versions = %#v", got)
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		for index, version := range got {
			want := first.Current.Ref.Version + uint64(index) + 1
			if version != want {
				t.Fatalf("versions = %#v, want contiguous sequence ending at %d", got, want)
			}
		}
	})

	t.Run("deletion is guarded atomic and tombstoned", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		ref := conformanceSkillRef("deletion")
		current := publishConformanceSkill(t, fixture, ref, conformanceSkillPackage("version-1"), true, "", "delete-1")
		for version := 2; version <= 5; version++ {
			validated := conformanceSkillPackage(fmt.Sprintf("version-%d", version))
			validated.Package.PlanningInstructions[0] += fmt.Sprintf(" Revision %d.", version)
			current = publishConformanceSkill(t, fixture, ref, validated, false, current.Current.RevisionToken, fmt.Sprintf("delete-%d", version))
		}
		_, err := fixture.Deletions.DeleteVersions(t.Context(), orchestration.DeleteSkillVersionsInput{
			Ref: ref, FromVersion: 3, ToVersion: 4,
			ExpectedRevisionToken: current.Current.RevisionToken, Reason: "unsafe range",
		})
		if !errors.Is(err, orchestration.ErrSkillConflict) {
			t.Fatalf("protected deletion error = %v, want ErrSkillConflict", err)
		}
		if _, err := fixture.Revisions.GetVersion(t.Context(), ref, 3); err != nil {
			t.Fatalf("atomic protected rejection removed version 3: %v", err)
		}

		deleted, err := fixture.Deletions.DeleteVersions(t.Context(), orchestration.DeleteSkillVersionsInput{
			Ref: ref, FromVersion: 1, ToVersion: 3,
			ExpectedRevisionToken: current.Current.RevisionToken, Reason: "bounded retention",
		})
		if err != nil || deleted.Outcome != orchestration.SkillAuditDeleted ||
			!reflectUint64SetEqual(deleted.DeletedVersions, []uint64{1, 2, 3}) ||
			deleted.CurrentPublished != current.Current.Ref {
			t.Fatalf("DeleteVersions() = %#v, %v", deleted, err)
		}
		for version := uint64(1); version <= 3; version++ {
			if _, err := fixture.Revisions.GetVersion(t.Context(), ref, version); !errors.Is(err, orchestration.ErrSkillRevisionNotFound) {
				t.Fatalf("GetVersion(%d) error = %v, want ErrSkillRevisionNotFound", version, err)
			}
		}
		assertCandidateStatus(t, fixture.Registry, orchestration.SkillCandidateRequest{
			Ref: ref, RequestedVersion: "2",
		}, orchestration.SkillCandidateDeleted)

		repeated, err := fixture.Deletions.DeleteVersions(t.Context(), orchestration.DeleteSkillVersionsInput{
			Ref: ref, FromVersion: 1, ToVersion: 3,
			ExpectedRevisionToken: current.Current.RevisionToken, Reason: "retry",
		})
		if err != nil || repeated.Outcome != orchestration.SkillAuditDeleteNoOp ||
			!reflectUint64SetEqual(repeated.AlreadyDeletedVersions, []uint64{1, 2, 3}) {
			t.Fatalf("idempotent deletion = %#v, %v", repeated, err)
		}
		page, err := fixture.Revisions.ListVersions(t.Context(), ref, orchestration.SkillVersionListOptions{Limit: 10})
		if err != nil || len(page.Versions) != 5 {
			t.Fatalf("ListVersions() = %#v, %v", page, err)
		}
		for _, summary := range page.Versions {
			if summary.Ref.Version <= 3 && summary.Status != orchestration.SkillRevisionDeleted {
				t.Fatalf("tombstone summary = %#v", summary)
			}
		}
	})

	t.Run("publication deletion race preserves a coherent current revision", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		ref := conformanceSkillRef("publish-delete-race")
		current := publishConformanceSkill(t, fixture, ref, conformanceSkillPackage("race-1"), true, "", "race-1")
		for version := 2; version <= 3; version++ {
			validated := conformanceSkillPackage(fmt.Sprintf("race-%d", version))
			validated.Package.PlanningInstructions[0] += fmt.Sprintf(" Revision %d.", version)
			current = publishConformanceSkill(
				t, fixture, ref, validated, false, current.Current.RevisionToken, fmt.Sprintf("race-%d", version),
			)
		}
		validated := conformanceSkillPackage("race-4")
		validated.Package.PlanningInstructions[0] += " Revision 4."
		start := make(chan struct{})
		publishResult := make(chan orchestration.PutPublishedSkillResult, 1)
		publishErr := make(chan error, 1)
		deleteErr := make(chan error, 1)
		go func() {
			<-start
			result, err := fixture.Administration.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
				Ref: ref, Package: validated, ExpectedRevisionToken: current.Current.RevisionToken,
				IdempotencyKey: "race-4",
			})
			publishResult <- result
			publishErr <- err
		}()
		go func() {
			<-start
			_, err := fixture.Deletions.DeleteVersions(t.Context(), orchestration.DeleteSkillVersionsInput{
				Ref: ref, FromVersion: 2, ToVersion: 2,
				ExpectedRevisionToken: current.Current.RevisionToken, Reason: "race cleanup",
			})
			deleteErr <- err
		}()
		close(start)
		published, err := <-publishResult, <-publishErr
		if err != nil || published.Current.Ref.Version != 4 {
			t.Fatalf("raced publication = %#v, %v", published, err)
		}
		deletionErr := <-deleteErr
		if !errors.Is(deletionErr, orchestration.ErrSkillProtectedRevision) &&
			!errors.Is(deletionErr, orchestration.ErrSkillPrecondition) {
			t.Fatalf("raced deletion error = %v", deletionErr)
		}
		latest, err := fixture.PeerRevisions.GetPublished(t.Context(), ref)
		if err != nil || latest.Revision.Ref != published.Current.Ref {
			t.Fatalf("current after race = %#v, %v", latest, err)
		}
		if _, versionErr := fixture.Revisions.GetVersion(t.Context(), ref, 2); versionErr != nil {
			t.Fatalf("raced deletion removed protected version 2: %v", versionErr)
		}
	})

	t.Run("not found cancellation and safe errors", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		missing := conformanceSkillRef("missing")
		if _, err := fixture.Revisions.GetPublished(t.Context(), missing); !errors.Is(err, orchestration.ErrSkillNotFound) {
			t.Fatalf("GetPublished(missing) error = %v", err)
		}
		if _, err := fixture.Registry.GetManifest(t.Context(), orchestration.SkillVersionRef{Ref: missing, Version: 1}); !errors.Is(err, orchestration.ErrSkillRevisionNotFound) {
			t.Fatalf("GetManifest(missing) error = %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := fixture.Registry.ResolveCandidates(ctx, []orchestration.SkillCandidateRequest{{Ref: missing, RequestedVersion: "published"}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ResolveCandidates(cancelled) error = %v", err)
		}
		deadlineCtx, deadlineCancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
		defer deadlineCancel()
		_, err = fixture.Registry.ResolveCandidates(deadlineCtx, []orchestration.SkillCandidateRequest{{Ref: missing, RequestedVersion: "published"}})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ResolveCandidates(expired deadline) error = %v", err)
		}

		secret := "api_key=conformance-super-secret"
		published := publishConformanceSkill(t, fixture, missing, conformanceSkillPackage("safe-error"), true, "", "safe-error-create")
		_, err = fixture.Administration.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
			Ref: missing, Package: conformanceSkillPackage("safe-error-update"),
			ExpectedRevisionToken: secret, IdempotencyKey: "safe-error-update",
		})
		if !errors.Is(err, orchestration.ErrSkillConflict) {
			t.Fatalf("unsafe precondition error = %v, want ErrSkillConflict (published %#v)", err, published)
		}
		if strings.Contains(err.Error(), "conformance-super-secret") {
			t.Fatalf("provider error leaked credential: %v", err)
		}
	})

	t.Run("corruption returns stable integrity errors without bodies", func(t *testing.T) {
		fixture := requireSkillFixture(t, factory(t))
		if fixture.CorruptManifest == nil || fixture.CorruptResource == nil {
			t.Fatal("corruption callbacks are required by skill conformance")
		}
		ref := conformanceSkillRef("integrity")
		published := publishConformanceSkill(t, fixture, ref, conformanceSkillPackage("sensitive-body-marker"), true, "", "integrity-1")
		manifest, err := fixture.Registry.GetManifest(t.Context(), published.Current.Ref)
		if err != nil || len(manifest.Resources) != 1 {
			t.Fatalf("GetManifest() = %#v, %v", manifest, err)
		}
		resourceRef := orchestration.SkillResourceRef{
			Skill: published.Current.Ref, Name: manifest.Resources[0].Name,
			ExpectedHash: manifest.Resources[0].ResourceHash,
		}
		if err := fixture.CorruptResource(resourceRef); err != nil {
			t.Fatalf("CorruptResource() error = %v", err)
		}
		if _, err := fixture.Registry.GetResource(t.Context(), resourceRef); !errors.Is(err, orchestration.ErrSkillIntegrity) ||
			strings.Contains(err.Error(), "sensitive-body-marker") {
			t.Fatalf("GetResource(corrupt) error = %v", err)
		}
		if err := fixture.CorruptManifest(published.Current.Ref); err != nil {
			t.Fatalf("CorruptManifest() error = %v", err)
		}
		if _, err := fixture.Registry.GetManifest(t.Context(), published.Current.Ref); !errors.Is(err, orchestration.ErrSkillIntegrity) ||
			strings.Contains(err.Error(), "sensitive-body-marker") {
			t.Fatalf("GetManifest(corrupt) error = %v", err)
		}
	})
}

func requireSkillFixture(t *testing.T, fixture SkillFixture) SkillFixture {
	t.Helper()
	values := map[string]interface{}{
		"registry": fixture.Registry, "peer registry": fixture.PeerRegistry,
		"revision reader": fixture.Revisions, "peer revision reader": fixture.PeerRevisions,
		"administration": fixture.Administration, "deletions": fixture.Deletions,
	}
	for name, value := range values {
		if value == nil {
			t.Fatalf("skill fixture %s capability is nil", name)
		}
	}
	return fixture
}

func publishConformanceSkill(
	t *testing.T,
	fixture SkillFixture,
	ref orchestration.SkillRef,
	validated orchestration.ValidatedSkillPackage,
	requireAbsent bool,
	expectedToken string,
	idempotencyKey string,
) orchestration.PutPublishedSkillResult {
	t.Helper()
	result, err := fixture.Administration.PutPublished(t.Context(), orchestration.PutPublishedSkillInput{
		Ref: ref, Package: validated, RequireAbsent: requireAbsent,
		ExpectedRevisionToken: expectedToken, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		t.Fatalf("PutPublished(%s) error = %v", ref.String(), err)
	}
	return result
}

func conformanceSkillRef(name string) orchestration.SkillRef {
	return orchestration.SkillRef{Namespace: "conformance", Name: name}
}

func conformanceSkillPackage(reason string) orchestration.ValidatedSkillPackage {
	return orchestration.ValidatedSkillPackage{Package: orchestration.SkillPackageInput{
		DisplayName: "Conformance Skill",
		Description: "Assess test conditions. Use when a conformance request requires this skill.",
		Domains:     []string{"testing"},
		Tags:        []string{"conformance"},
		PlanningInstructions: []string{
			"Establish the conformance input and expected outcome.",
		},
		ResponseInstructions: []string{"Report the verified outcome."},
		Resources: []orchestration.SkillResourceInput{{
			Name: "details", Description: "Conditional conformance details.",
			LoadWhen:    "The request asks for detailed conformance evidence.",
			AppliesTo:   []orchestration.SkillResourceScope{orchestration.SkillResourceContinuation},
			ContentType: "text/plain", Content: "Detailed guidance for " + reason + ".",
		}},
		ActivationExamples: orchestration.SkillActivationExamples{
			ShouldActivate: []string{"Run the skill conformance check."},
		},
		ChangeReason: reason,
	}}
}

func cloneConformanceSkillPackage(input orchestration.SkillPackageInput) orchestration.SkillPackageInput {
	cloned := input
	cloned.Domains = append([]string(nil), input.Domains...)
	cloned.Tags = append([]string(nil), input.Tags...)
	cloned.PlanningInstructions = append([]string(nil), input.PlanningInstructions...)
	cloned.ResponseInstructions = append([]string(nil), input.ResponseInstructions...)
	cloned.ToolHints = append([]string(nil), input.ToolHints...)
	cloned.ActivationExamples.ShouldActivate = append([]string(nil), input.ActivationExamples.ShouldActivate...)
	cloned.ActivationExamples.ShouldNotActivate = append([]string(nil), input.ActivationExamples.ShouldNotActivate...)
	cloned.Resources = make([]orchestration.SkillResourceInput, len(input.Resources))
	for index, resource := range input.Resources {
		cloned.Resources[index] = resource
		cloned.Resources[index].AppliesTo = append([]orchestration.SkillResourceScope(nil), resource.AppliesTo...)
	}
	return cloned
}

func assertCandidateResolution(
	t *testing.T,
	registry orchestration.SkillRegistry,
	requests []orchestration.SkillCandidateRequest,
	want map[string]orchestration.SkillVersionRef,
) {
	t.Helper()
	candidates, err := registry.ResolveCandidates(t.Context(), requests)
	if err != nil {
		t.Fatalf("ResolveCandidates() error = %v", err)
	}
	keyed := keySkillCandidates(candidates)
	for key, ref := range want {
		assertResolvedCandidate(t, keyed[key], ref)
	}
}

func assertCandidateStatus(
	t *testing.T,
	registry orchestration.SkillRegistry,
	request orchestration.SkillCandidateRequest,
	want orchestration.SkillCandidateStatus,
) {
	t.Helper()
	candidates, err := registry.ResolveCandidates(t.Context(), []orchestration.SkillCandidateRequest{request})
	if err != nil || len(candidates) != 1 || candidates[0].Status != want {
		t.Fatalf("ResolveCandidates() = %#v, %v; want status %q", candidates, err, want)
	}
}

func keySkillCandidates(candidates []orchestration.SkillCandidate) map[string]orchestration.SkillCandidate {
	keyed := make(map[string]orchestration.SkillCandidate, len(candidates))
	for _, candidate := range candidates {
		keyed[candidate.Ref.String()+"@"+candidate.RequestedVersion] = candidate
	}
	return keyed
}

func assertResolvedCandidate(t *testing.T, candidate orchestration.SkillCandidate, want orchestration.SkillVersionRef) {
	t.Helper()
	if candidate.Status != orchestration.SkillCandidateResolved || candidate.Resolved != want ||
		candidate.Metadata.Ref != want.Ref || candidate.Metadata.PublishedVersion != want.Version {
		t.Fatalf("resolved candidate = %#v, want %#v", candidate, want)
	}
}

func sameCandidateIdentitySet(left, right []orchestration.SkillCandidate) bool {
	if len(left) != len(right) {
		return false
	}
	leftKeyed := keySkillCandidates(left)
	rightKeyed := keySkillCandidates(right)
	if len(leftKeyed) != len(rightKeyed) {
		return false
	}
	for key, leftCandidate := range leftKeyed {
		rightCandidate, found := rightKeyed[key]
		if !found || leftCandidate.Status != rightCandidate.Status || leftCandidate.Resolved != rightCandidate.Resolved {
			return false
		}
	}
	return true
}

func reflectUint64SetEqual(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]uint64(nil), left...)
	right = append([]uint64(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	sort.Slice(right, func(i, j int) bool { return right[i] < right[j] })
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
