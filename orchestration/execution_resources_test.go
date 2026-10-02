package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type resourceLeaseProvider struct {
	core.MockInvestigationCoordinator
	acquire func(context.Context, core.InvestigationLeaseRequest) (*core.InvestigationLease, string, error)
	release func(context.Context, core.InvestigationLease) (core.InvestigationReleaseOutcome, error)
}

func (p *resourceLeaseProvider) AcquireInvestigation(ctx context.Context, r core.InvestigationLeaseRequest) (*core.InvestigationLease, string, error) {
	if p.acquire != nil {
		return p.acquire(ctx, r)
	}
	return &core.InvestigationLease{AgentName: r.AgentName, EntityID: r.EntityID, AcquisitionID: r.AcquisitionID, ExpiresAt: time.Now().Add(time.Minute)}, "", nil
}
func (p *resourceLeaseProvider) ReleaseInvestigationLease(ctx context.Context, l core.InvestigationLease) (core.InvestigationReleaseOutcome, error) {
	if p.release != nil {
		return p.release(ctx, l)
	}
	return core.InvestigationReleased, nil
}

type resourceEndHook struct {
	end func(context.Context) (*core.PipelineShortCircuit, error)
}

func (*resourceEndHook) Name() string { return "resource-test-end" }
func (h *resourceEndHook) BeforePlanning(ctx context.Context, _ *core.PipelineContext) (*core.PipelineShortCircuit, error) {
	return h.end(ctx)
}

func TestExecutionResourcesAllRequestExits(t *testing.T) {
	for _, mode := range []string{"short_circuit", "planning_failure", "cancelled", "panic", "stream_delivery_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			released := 0
			provider := &resourceLeaseProvider{release: func(ctx context.Context, lease core.InvestigationLease) (core.InvestigationReleaseOutcome, error) {
				released++
				if ctx.Err() != nil || lease.EntityID != "document" || GetRequestID(ctx) == "" {
					t.Error("cleanup lost context/ownership")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("cleanup not bounded")
				}
				return core.InvestigationReleased, nil
			}}
			hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, provider, "agent", "docs")
			if err != nil {
				t.Fatal(err)
			}
			activity := &core.MockActivityCoordinator{}
			announcement, err := NewActivityAnnouncementHook(activity, "agent", "docs", 10)
			if err != nil {
				t.Fatal(err)
			}
			o := NewAIOrchestrator(NewDefaultOrchestratorConfig(), nil, nil)
			store := &terminalRecordStore{}
			o.SetExecutionStore(store)
			o.pipelineHooks = []core.PipelineHook{announcement, hook, &resourceEndHook{end: func(context.Context) (*core.PipelineShortCircuit, error) {
				switch mode {
				case "panic":
					panic("original panic")
				case "cancelled":
					cancel()
					return nil, nil
				case "planning_failure":
					return nil, nil
				default:
					return &core.PipelineShortCircuit{Response: "available", Source: "fixture"}, nil
				}
			}}}
			metadata := map[string]interface{}{"entity_type": "document", "entity_id": "document"}
			var runErr error
			panicValue := caughtPanic(func() {
				if mode == "stream_delivery_failure" {
					_, runErr = o.ProcessRequestStreaming(ctx, "request", metadata, func(core.StreamChunk) error { return errors.New("delivery stopped") })
				} else {
					_, runErr = o.ProcessRequest(ctx, "request", metadata)
				}
			})
			if mode == "panic" && panicValue != "original panic" {
				t.Fatalf("panic changed: %v", panicValue)
			}
			if mode != "panic" && panicValue != nil {
				t.Fatalf("unexpected panic: %v", panicValue)
			}
			if mode != "panic" && mode != "short_circuit" && runErr == nil {
				t.Fatal("original failure lost")
			}
			if mode == "short_circuit" && runErr != nil {
				t.Fatal(runErr)
			}
			if released != 1 {
				t.Fatalf("release count %d", released)
			}
			if activity.CompleteActivityCt != 1 {
				t.Fatal("activity not removed on request exit")
			}
			shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err := o.Shutdown(shutdownCtx); err != nil {
				t.Fatal(err)
			}
			records, _ := store.snapshot()
			if len(records) == 0 {
				t.Fatal("cleanup evidence not persisted")
			}
			wantLifecycle := RequestLifecycleFailed
			if mode == "short_circuit" {
				wantLifecycle = RequestLifecycleCompleted
			}
			if mode == "cancelled" {
				wantLifecycle = RequestLifecycleCancelled
			}
			last := records[len(records)-1]
			if last.Lifecycle == nil || last.Lifecycle.State != wantLifecycle || last.RecordRevision == 0 {
				t.Fatalf("wrong terminal lifecycle: %+v", last.Lifecycle)
			}
			found := false
			for _, invocation := range records[len(records)-1].PipelineHooks {
				for _, effect := range invocation.Effects {
					if strings.HasPrefix(effect.EffectID, "investigation_lease_cleanup:") {
						found = true
						if effect.Status != core.PipelineHookEffectSucceeded || !strings.Contains(string(effect.Data), `"outcome":"released"`) {
							t.Fatalf("cleanup evidence: %+v", effect)
						}
					}
				}
			}
			if !found {
				t.Fatal("terminal record omitted cleanup effect")
			}
		})
	}
}

func TestExecutionResourcesProviderFailuresAndClose(t *testing.T) {
	for _, mode := range []string{"error", "panic", "unknown", "invalid_outcome", "deadline", "not_owned", "absent"} {
		t.Run(mode, func(t *testing.T) {
			resources := &executionResources{}
			reporter := &captureEffectReporter{}
			ctx := core.WithPipelineHookEffectReporter(t.Context(), reporter)
			calls := 0
			provider := &resourceLeaseProvider{release: func(ctx context.Context, _ core.InvestigationLease) (core.InvestigationReleaseOutcome, error) {
				calls++
				switch mode {
				case "panic":
					panic("provider failure")
				case "error":
					return core.InvestigationUnknown, errors.New("backend error")
				case "unknown":
					return core.InvestigationUnknown, nil
				case "invalid_outcome":
					return "provider text", nil
				case "deadline":
					<-ctx.Done()
					return core.InvestigationUnknown, ctx.Err()
				case "not_owned":
					return core.InvestigationNotOwned, nil
				default:
					return core.InvestigationAbsent, nil
				}
			}}
			if ok, _, err := resources.acquire(ctx, provider, "agent", "entity"); !ok || err != nil {
				t.Fatal(err)
			}
			resources.close(ctx, time.Millisecond, &core.NoOpLogger{})
			resources.close(ctx, time.Millisecond, nil)
			if calls != 1 || len(reporter.effects) != 2 {
				t.Fatalf("cleanup duplicated/missing: %d %+v", calls, reporter.effects)
			}
			if ok, _, err := resources.acquire(ctx, provider, "agent", "entity"); ok || err == nil {
				t.Fatal("closed scope accepted acquisition")
			}
			want := core.PipelineHookEffectFailed
			if mode == "not_owned" || mode == "absent" {
				want = core.PipelineHookEffectSucceeded
			}
			if reporter.effects[1].Status != want {
				t.Fatal("unknown cleanup reported success")
			}
		})
	}
	for _, mode := range []string{"error", "contended", "invalid_handle"} {
		t.Run(mode, func(t *testing.T) {
			resources := &executionResources{}
			provider := &resourceLeaseProvider{acquire: func(context.Context, core.InvestigationLeaseRequest) (*core.InvestigationLease, string, error) {
				switch mode {
				case "error":
					return nil, "", errors.New("unknown acquisition")
				case "contended":
					return nil, "other", nil
				default:
					return &core.InvestigationLease{}, "", nil
				}
			}}
			if ok, _, _ := resources.acquire(t.Context(), provider, "agent", "entity"); ok || len(resources.leases) != 0 {
				t.Fatal("unconfirmed acquisition registered")
			}
			resources.close(t.Context(), time.Second, nil)
		})
	}
}

func TestExecutionCleanupTimeoutConfiguration(t *testing.T) {
	for _, mode := range []EnvironmentMode{EnvironmentStrict, EnvironmentCompatible} {
		for _, raw := range []string{"", "0s", "-1s", "bad", "999999999999999999999h"} {
			_, err := ResolveOrchestratorConfig(ConfigResolution{Environment: mode, LookupEnv: lookupFromMap(map[string]string{"TRUVAG3_EXECUTION_CLEANUP_TIMEOUT": raw})})
			if err == nil {
				t.Fatalf("mode=%v accepted %q", mode, raw)
			}
		}
	}
	for _, bad := range []time.Duration{0, -time.Second} {
		if _, err := ResolveOrchestratorConfig(ConfigResolution{Options: []OrchestratorOption{WithExecutionCleanupTimeout(bad)}}); err == nil {
			t.Fatal("invalid explicit option accepted")
		}
	}
	result, err := ResolveOrchestratorConfig(ConfigResolution{Environment: EnvironmentStrict, LookupEnv: lookupFromMap(map[string]string{"TRUVAG3_EXECUTION_CLEANUP_TIMEOUT": "8s"}), Options: []OrchestratorOption{WithExecutionCleanupTimeout(time.Second)}})
	if err != nil || result.Config.executionCleanupTimeout != time.Second {
		t.Fatalf("override failed: %v", err)
	}
}

func TestExecutionResourcesResumeReacquiresFreshOwnership(t *testing.T) {
	var acquisitions []string
	releases := 0
	provider := &resourceLeaseProvider{acquire: func(_ context.Context, r core.InvestigationLeaseRequest) (*core.InvestigationLease, string, error) {
		if r.EntityID != "document" {
			t.Error("resume lost checkpoint entity")
		}
		acquisitions = append(acquisitions, r.AcquisitionID)
		return &core.InvestigationLease{AgentName: r.AgentName, EntityID: r.EntityID, AcquisitionID: r.AcquisitionID, ExpiresAt: time.Now().Add(time.Minute)}, "", nil
	}, release: func(context.Context, core.InvestigationLease) (core.InvestigationReleaseOutcome, error) {
		releases++
		return core.InvestigationReleased, nil
	}}
	hook, err := NewMemoryEnrichmentHook(&core.MockEpisodicMemory{}, provider, "agent", "docs")
	if err != nil {
		t.Fatal(err)
	}
	o := NewAIOrchestrator(NewDefaultOrchestratorConfig(), nil, nil)
	t.Cleanup(func() { _ = o.Shutdown(context.Background()) })
	o.pipelineHooks = []core.PipelineHook{hook, &resourceEndHook{end: func(context.Context) (*core.PipelineShortCircuit, error) {
		return &core.PipelineShortCircuit{Response: "segment complete"}, nil
	}}}
	checkpoint := &ExecutionCheckpoint{CheckpointID: "checkpoint", Status: CheckpointStatusApproved, UserContext: map[string]interface{}{"entity_type": "document", "entity_id": "document"}}
	for range 2 {
		ctx, end, err := BuildResumeContext(t.Context(), checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		_, err = o.ProcessRequest(ctx, "resume", nil)
		end()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(acquisitions) != 2 || acquisitions[0] == acquisitions[1] || releases != 2 {
		t.Fatalf("segment ownership reused: acquisitions=%d releases=%d", len(acquisitions), releases)
	}
	if _, ok := checkpoint.UserContext["acquisition_id"]; ok {
		t.Fatal("private ownership leaked into checkpoint")
	}
}
