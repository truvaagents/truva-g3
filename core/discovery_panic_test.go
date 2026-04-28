package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Test basic discovery operations don't panic
func TestMockDiscovery_BasicOperations(t *testing.T) {
	md := NewMockDiscovery()

	// Register a service
	service := &ServiceRegistration{
		ID:      "test-service",
		Name:    "test",
		Address: "localhost",
		Port:    8080,
		Health:  HealthHealthy,
	}

	ctx := context.Background()
	err := md.Register(ctx, service)
	if err != nil {
		t.Fatalf("Failed to register service: %v", err)
	}

	// Find the service
	services, err := md.FindService(ctx, "test")
	if err != nil {
		t.Fatalf("Failed to find service: %v", err)
	}

	if len(services) != 1 {
		t.Errorf("Expected 1 service, got %d", len(services))
	}

	// Unregister the service
	err = md.Unregister(ctx, "test-service")
	if err != nil {
		t.Fatalf("Failed to unregister service: %v", err)
	}

	// Should not find after unregister
	services, err = md.FindService(ctx, "test")
	if err != nil {
		t.Fatalf("Failed to find service: %v", err)
	}

	if len(services) != 0 {
		t.Errorf("Expected 0 services after unregister, got %d", len(services))
	}
}

// Test concurrent registrations don't cause panics
func TestMockDiscovery_ConcurrentRegistrations(t *testing.T) {
	md := NewMockDiscovery()
	ctx := context.Background()

	var wg sync.WaitGroup
	numServices := 100

	// Concurrent registrations
	for i := 0; i < numServices; i++ {
		wg.Add(1)
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Registration %d panic: %v", idx, r)
				}
				wg.Done()
			}()

			service := &ServiceRegistration{
				ID:      fmt.Sprintf("service-%d", idx),
				Name:    fmt.Sprintf("service-%d", idx),
				Address: "localhost",
				Port:    8080 + idx,
				Health:  HealthHealthy,
			}

			err := md.Register(ctx, service)
			if err != nil {
				t.Logf("Registration %d error: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify all services were registered
	for i := 0; i < numServices; i++ {
		services, err := md.FindService(ctx, fmt.Sprintf("service-%d", i))
		if err != nil {
			t.Errorf("Failed to find service-%d: %v", i, err)
			continue
		}
		if len(services) != 1 {
			t.Errorf("Expected 1 service-%d, got %d", i, len(services))
		}
	}
}

// Test concurrent finds don't cause panics
func TestMockDiscovery_ConcurrentFinds(t *testing.T) {
	md := NewMockDiscovery()
	ctx := context.Background()

	// Register some services first
	for i := 0; i < 10; i++ {
		service := &ServiceRegistration{
			ID:      fmt.Sprintf("service-%d", i),
			Name:    "test-service",
			Address: "localhost",
			Port:    8080 + i,
			Health:  HealthHealthy,
		}
		err := md.Register(ctx, service)
		if err != nil {
			t.Fatalf("Failed to register service: %v", err)
		}
	}

	var wg sync.WaitGroup
	numFinds := 100

	// Concurrent finds
	for i := 0; i < numFinds; i++ {
		wg.Add(1)
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Find %d panic: %v", idx, r)
				}
				wg.Done()
			}()

			services, err := md.FindService(ctx, "test-service")
			if err != nil {
				t.Logf("Find %d error: %v", idx, err)
			}
			if len(services) != 10 {
				t.Errorf("Find %d: expected 10 services, got %d", idx, len(services))
			}
		}(i)
	}

	wg.Wait()
}

// Test concurrent mixed operations don't cause panics
func TestMockDiscovery_ConcurrentMixedOperations(t *testing.T) {
	md := NewMockDiscovery()
	ctx := context.Background()

	var wg sync.WaitGroup

	// Register operations
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Register %d panic: %v", idx, r)
				}
				wg.Done()
			}()

			service := &ServiceRegistration{
				ID:      fmt.Sprintf("service-%d", idx),
				Name:    "mixed-service",
				Address: "localhost",
				Port:    8080 + idx,
			}
			_ = md.Register(ctx, service)
		}(i)
	}

	// Find operations
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Find %d panic: %v", idx, r)
				}
				wg.Done()
			}()

			_, _ = md.FindService(ctx, "mixed-service")
		}(i)
	}

	// Unregister operations
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Unregister %d panic: %v", idx, r)
				}
				wg.Done()
			}()

			// Some might not exist, that's OK
			_ = md.Unregister(ctx, fmt.Sprintf("service-%d", idx))
		}(i)
	}

	wg.Wait()
}

// Test FindByCapability operations
func TestMockDiscovery_FindByCapability(t *testing.T) {
	md := NewMockDiscovery()
	ctx := context.Background()

	// Register services with capabilities
	for i := 0; i < 5; i++ {
		service := &ServiceRegistration{
			ID:      fmt.Sprintf("cap-service-%d", i),
			Name:    fmt.Sprintf("cap-service-%d", i),
			Address: "localhost",
			Port:    8080 + i,
			Capabilities: []Capability{
				{Name: "test-cap"},
				{Name: "other-cap"},
			},
		}
		err := md.Register(ctx, service)
		if err != nil {
			t.Fatalf("Failed to register service: %v", err)
		}
	}

	// Find by capability
	services, err := md.FindByCapability(ctx, "test-cap")
	if err != nil {
		t.Fatalf("Failed to find by capability: %v", err)
	}

	if len(services) != 5 {
		t.Errorf("Expected 5 services with capability, got %d", len(services))
	}

	// Find non-existent capability
	services, err = md.FindByCapability(ctx, "non-existent")
	if err != nil {
		t.Fatalf("Failed to find by capability: %v", err)
	}

	if len(services) != 0 {
		t.Errorf("Expected 0 services with non-existent capability, got %d", len(services))
	}
}

// Test nil context handling
func TestMockDiscovery_NilContext(t *testing.T) {
	md := NewMockDiscovery()

	service := &ServiceRegistration{
		ID:   "test",
		Name: "test",
	}

	// These should handle nil context gracefully
	var nilCtx context.Context

	// Test Register with nil context
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Register with nil context panic: %v", r)
			}
		}()
		_ = md.Register(nilCtx, service)
	}()

	// Test FindService with nil context
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("FindService with nil context panic: %v", r)
			}
		}()
		_, _ = md.FindService(nilCtx, "test")
	}()

	// Test Unregister with nil context
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Unregister with nil context panic: %v", r)
			}
		}()
		_ = md.Unregister(nilCtx, "test")
	}()

	// Test FindByCapability with nil context
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("FindByCapability with nil context panic: %v", r)
			}
		}()
		_, _ = md.FindByCapability(nilCtx, "test")
	}()
}

// Test with nil service registration
func TestMockDiscovery_NilService(t *testing.T) {
	md := NewMockDiscovery()
	ctx := context.Background()

	// Register nil service should handle gracefully
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Register nil service panic: %v", r)
			}
		}()
		err := md.Register(ctx, nil)
		if err == nil {
			t.Error("Expected error for nil service registration")
		}
	}()
}

// Test rapid register/unregister cycles
func TestMockDiscovery_RapidCycles(t *testing.T) {
	md := NewMockDiscovery()
	ctx := context.Background()

	service := &ServiceRegistration{
		ID:      "rapid-test",
		Name:    "rapid-test",
		Address: "localhost",
		Port:    8080,
	}

	// Rapid register/unregister cycles
	for i := 0; i < 100; i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Cycle %d panic: %v", i, r)
				}
			}()

			err := md.Register(ctx, service)
			if err != nil {
				t.Logf("Register error in cycle %d: %v", i, err)
			}

			_, err = md.FindService(ctx, "rapid-test")
			if err != nil {
				t.Logf("Find error in cycle %d: %v", i, err)
			}

			err = md.Unregister(ctx, "rapid-test")
			if err != nil {
				t.Logf("Unregister error in cycle %d: %v", i, err)
			}
		}()
	}
}

// Benchmark discovery operations
func BenchmarkMockDiscovery_Register(b *testing.B) {
	md := NewMockDiscovery()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		service := &ServiceRegistration{
			ID:      fmt.Sprintf("bench-%d", i),
			Name:    fmt.Sprintf("bench-%d", i),
			Address: "localhost",
			Port:    8080 + (i % 1000),
		}
		_ = md.Register(ctx, service)
	}
}

func BenchmarkMockDiscovery_Find(b *testing.B) {
	md := NewMockDiscovery()
	ctx := context.Background()

	// Pre-register services
	for i := 0; i < 1000; i++ {
		service := &ServiceRegistration{
			ID:      fmt.Sprintf("bench-%d", i),
			Name:    "bench-service",
			Address: "localhost",
			Port:    8080 + i,
		}
		_ = md.Register(ctx, service)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = md.FindService(ctx, "bench-service")
	}
}

func BenchmarkMockDiscovery_ConcurrentOps(b *testing.B) {
	md := NewMockDiscovery()
	ctx := context.Background()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			service := &ServiceRegistration{
				ID:      fmt.Sprintf("concurrent-%d-%d", i, time.Now().UnixNano()),
				Name:    "concurrent-service",
				Address: "localhost",
				Port:    8080 + (i % 1000),
			}
			_ = md.Register(ctx, service)
			_, _ = md.FindService(ctx, "concurrent-service")
			i++
		}
	})
}
