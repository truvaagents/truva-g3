package orchestration

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWithStepCallback verifies that WithStepCallback correctly attaches a callback to context
func TestWithStepCallback(t *testing.T) {
	t.Run("nil callback from empty context", func(t *testing.T) {
		ctx := context.Background()
		cb := GetStepCallback(ctx)
		if cb != nil {
			t.Error("expected nil callback from empty context")
		}
	})

	t.Run("callback is retrievable after set", func(t *testing.T) {
		ctx := context.Background()
		called := false

		cb := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			called = true
		}

		ctx = WithStepCallback(ctx, cb)
		retrieved := GetStepCallback(ctx)

		if retrieved == nil {
			t.Fatal("expected non-nil callback after WithStepCallback")
		}

		// Invoke and verify it's the same callback
		retrieved(0, 1, RoutingStep{AgentName: "test"}, StepResult{Success: true})
		if !called {
			t.Error("callback was not invoked")
		}
	})

	t.Run("nil callback can be set", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithStepCallback(ctx, nil)

		// Should return nil without panic
		cb := GetStepCallback(ctx)
		if cb != nil {
			t.Error("expected nil callback when nil was set")
		}
	})

	t.Run("callback receives correct parameters", func(t *testing.T) {
		ctx := context.Background()

		var receivedStepIndex, receivedTotalSteps int
		var receivedStep RoutingStep
		var receivedResult StepResult

		cb := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			receivedStepIndex = stepIndex
			receivedTotalSteps = totalSteps
			receivedStep = step
			receivedResult = result
		}

		ctx = WithStepCallback(ctx, cb)
		retrieved := GetStepCallback(ctx)

		testStep := RoutingStep{
			StepID:    "step_1",
			AgentName: "weather-tool",
			Namespace: "travel",
		}
		testResult := StepResult{
			StepID:    "step_1",
			AgentName: "weather-tool",
			Success:   true,
			Duration:  100 * time.Millisecond,
		}

		retrieved(2, 5, testStep, testResult)

		if receivedStepIndex != 2 {
			t.Errorf("expected stepIndex=2, got %d", receivedStepIndex)
		}
		if receivedTotalSteps != 5 {
			t.Errorf("expected totalSteps=5, got %d", receivedTotalSteps)
		}
		if receivedStep.AgentName != "weather-tool" {
			t.Errorf("expected AgentName='weather-tool', got %s", receivedStep.AgentName)
		}
		if !receivedResult.Success {
			t.Error("expected Success=true")
		}
	})

	t.Run("context inheritance preserves callback", func(t *testing.T) {
		ctx := context.Background()
		called := false

		cb := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			called = true
		}

		ctx = WithStepCallback(ctx, cb)

		// Create child context with timeout
		childCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()

		// Callback should be retrievable from child context
		retrieved := GetStepCallback(childCtx)
		if retrieved == nil {
			t.Fatal("callback not inherited by child context")
		}

		retrieved(0, 1, RoutingStep{}, StepResult{})
		if !called {
			t.Error("inherited callback was not invoked")
		}
	})

	t.Run("callback can be overridden in child context", func(t *testing.T) {
		ctx := context.Background()
		parentCalled := false
		childCalled := false

		parentCb := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			parentCalled = true
		}
		childCb := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			childCalled = true
		}

		ctx = WithStepCallback(ctx, parentCb)
		childCtx := WithStepCallback(ctx, childCb)

		// Parent context should still have parent callback
		parentRetrieved := GetStepCallback(ctx)
		parentRetrieved(0, 1, RoutingStep{}, StepResult{})
		if !parentCalled {
			t.Error("parent callback was not invoked")
		}

		// Child context should have child callback
		childRetrieved := GetStepCallback(childCtx)
		childRetrieved(0, 1, RoutingStep{}, StepResult{})
		if !childCalled {
			t.Error("child callback was not invoked")
		}
	})
}

// TestStepCallbackConcurrency verifies thread-safety of context-based callbacks
func TestStepCallbackConcurrency(t *testing.T) {
	t.Run("concurrent reads are safe", func(t *testing.T) {
		ctx := context.Background()
		var callCount int64

		cb := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			atomic.AddInt64(&callCount, 1)
		}

		ctx = WithStepCallback(ctx, cb)

		// Spawn multiple goroutines reading and invoking the callback
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				retrieved := GetStepCallback(ctx)
				if retrieved != nil {
					retrieved(idx, 100, RoutingStep{}, StepResult{})
				}
			}(i)
		}

		wg.Wait()

		if atomic.LoadInt64(&callCount) != 100 {
			t.Errorf("expected 100 calls, got %d", callCount)
		}
	})

	t.Run("concurrent context creation is safe", func(t *testing.T) {
		baseCtx := context.Background()
		var wg sync.WaitGroup
		var callCount int64

		// Each goroutine creates its own child context with its own callback
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()

				cb := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
					atomic.AddInt64(&callCount, 1)
				}

				ctx := WithStepCallback(baseCtx, cb)
				retrieved := GetStepCallback(ctx)
				if retrieved != nil {
					retrieved(idx, 50, RoutingStep{}, StepResult{})
				}
			}(i)
		}

		wg.Wait()

		if atomic.LoadInt64(&callCount) != 50 {
			t.Errorf("expected 50 calls, got %d", callCount)
		}
	})
}

// TestStepCallbackIsolation verifies per-request isolation
func TestStepCallbackIsolation(t *testing.T) {
	t.Run("different requests have isolated callbacks", func(t *testing.T) {
		baseCtx := context.Background()

		request1Calls := make([]int, 0)
		request2Calls := make([]int, 0)
		var mu sync.Mutex

		cb1 := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			mu.Lock()
			request1Calls = append(request1Calls, stepIndex)
			mu.Unlock()
		}

		cb2 := func(stepIndex, totalSteps int, step RoutingStep, result StepResult) {
			mu.Lock()
			request2Calls = append(request2Calls, stepIndex)
			mu.Unlock()
		}

		ctx1 := WithStepCallback(baseCtx, cb1)
		ctx2 := WithStepCallback(baseCtx, cb2)

		// Simulate concurrent request processing
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			retrieved := GetStepCallback(ctx1)
			for i := 0; i < 3; i++ {
				retrieved(i, 3, RoutingStep{}, StepResult{})
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			retrieved := GetStepCallback(ctx2)
			for i := 0; i < 5; i++ {
				retrieved(i, 5, RoutingStep{}, StepResult{})
			}
		}()

		wg.Wait()

		if len(request1Calls) != 3 {
			t.Errorf("request1 expected 3 calls, got %d", len(request1Calls))
		}
		if len(request2Calls) != 5 {
			t.Errorf("request2 expected 5 calls, got %d", len(request2Calls))
		}
	})
}
