package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

func TestExecutionActivityRenewalKeepsLatestStatusAndJoins(t *testing.T) {
	var mu sync.Mutex
	var signals []core.ActivitySignal
	removed := false
	updates := make(chan struct{}, 100)
	provider := &core.MockActivityCoordinator{
		AnnounceActivityFn: func(ctx context.Context, signal core.ActivitySignal) error {
			mu.Lock()
			defer mu.Unlock()
			if removed {
				t.Error("renewal after removal")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("unbounded activity write")
			}
			signals = append(signals, signal)
			updates <- struct{}{}
			return nil
		},
		CompleteActivityFn: func(ctx context.Context, id string) error {
			mu.Lock()
			defer mu.Unlock()
			removed = true
			if ctx.Err() != nil || id != "request" {
				t.Error("cleanup context or identity lost")
			}
			return nil
		},
	}
	resources := &executionResources{timeout: time.Second}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), executionResourcesKey{}, resources))
	defer cancel()
	defer resources.close(ctx, time.Second, nil)
	signal := core.ActivitySignal{RequestID: "request", Status: "planning", StartedAt: time.Now(), TTL: 30 * time.Millisecond}
	if err := resources.announceActivity(ctx, provider, signal); err != nil {
		t.Fatal(err)
	}
	owner := managedActivity(ctx)
	if err := resources.announceActivity(ctx, provider, signal); err != nil || managedActivity(ctx) != owner {
		t.Fatal("duplicate owner")
	}
	if err := updateExecutionActivity(ctx, provider, "request", "synthesizing"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-updates:
		case <-time.After(time.Second):
			t.Fatal("renewal did not arrive")
		}
	}
	cleanup, _ := NewActivityCleanupHook(provider, &core.NoOpLogger{})
	if _, err := cleanup.AfterSynthesis(ctx, &core.PipelineContext{}, "response"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if removed {
		t.Error("phase hook removed signal before outer delivery")
	}
	mu.Unlock()
	cancel()
	resources.close(ctx, time.Second, nil)
	select {
	case <-owner.done:
	default:
		t.Fatal("renewal not joined")
	}
	mu.Lock()
	defer mu.Unlock()
	if !removed || len(signals) < 5 {
		t.Fatal("missing renewal or removal")
	}
	for _, got := range signals[1:] {
		if got.Status != "synthesizing" || got.RequestID != signal.RequestID || got.StartedAt != signal.StartedAt {
			t.Fatal("renewal regressed status or identity")
		}
	}
	if err := resources.announceActivity(ctx, provider, signal); err == nil {
		t.Fatal("closed scope accepted announcement")
	}
	if err := owner.publish(ctx, "executing"); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionActivityProviderFailuresAreBounded(t *testing.T) {
	for _, mode := range []string{"error", "panic", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			provider := &core.MockActivityCoordinator{AnnounceActivityFn: func(ctx context.Context, _ core.ActivitySignal) error {
				switch mode {
				case "panic":
					panic("provider panic")
				case "timeout":
					<-ctx.Done()
					return ctx.Err()
				default:
					return errors.New("offline")
				}
			}, CompleteActivityFn: func(context.Context, string) error {
				if mode == "panic" {
					panic("provider panic")
				}
				return errors.New("offline")
			}}
			resources := &executionResources{timeout: time.Millisecond}
			ctx := context.WithValue(t.Context(), executionResourcesKey{}, resources)
			if err := resources.announceActivity(ctx, provider, core.ActivitySignal{RequestID: "request", TTL: 6 * time.Millisecond}); err == nil {
				t.Fatal("submission failure hidden")
			}
			// Let at least one renewal observe the same failure, then join it.
			time.Sleep(12 * time.Millisecond)
			resources.close(ctx, time.Second, &core.NoOpLogger{})
			owner := managedActivity(ctx)
			if owner.errors < 1 {
				t.Fatal("failure count missing")
			}
		})
	}
	provider := &core.MockActivityCoordinator{}
	if err := updateExecutionActivity(t.Context(), provider, "legacy", "executing"); err != nil || provider.UpdateStatusCt != 1 {
		t.Fatal("legacy status path changed")
	}
	if got := (&executionResources{}).operationTimeout(); got != 5*time.Second {
		t.Fatal("default timeout changed")
	}
}
