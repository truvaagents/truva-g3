package core

import (
	"context"
	"fmt"
	"sync"
)

// MockDiscovery provides an in-memory mock implementation of Discovery
type MockDiscovery struct {
	mu           sync.RWMutex
	services     map[string]*ServiceInfo
	capabilities map[string][]string // capability -> service IDs
}

// NewMockDiscovery creates a new mock discovery instance
func NewMockDiscovery() *MockDiscovery {
	return &MockDiscovery{
		services:     make(map[string]*ServiceInfo),
		capabilities: make(map[string][]string),
	}
}

// Register registers a service (implements Registry interface)
func (m *MockDiscovery) Register(ctx context.Context, info *ServiceInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.services[info.ID] = info

	// Update capability index
	for _, cap := range info.Capabilities {
		if m.capabilities[cap.Name] == nil {
			m.capabilities[cap.Name] = []string{}
		}
		if !contains(m.capabilities[cap.Name], info.ID) {
			m.capabilities[cap.Name] = append(m.capabilities[cap.Name], info.ID)
		}
	}

	return nil
}

// UpdateHealth updates service health status (implements Registry interface)
func (m *MockDiscovery) UpdateHealth(ctx context.Context, id string, status HealthStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if service, exists := m.services[id]; exists {
		service.Health = status
		return nil
	}

	return fmt.Errorf("service %s not found", id)
}

// Unregister removes a service (implements Registry interface)
func (m *MockDiscovery) Unregister(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	service, exists := m.services[id]
	if !exists {
		return fmt.Errorf("service %s not found", id)
	}

	// Remove from capability index
	for _, cap := range service.Capabilities {
		m.capabilities[cap.Name] = removeString(m.capabilities[cap.Name], id)
		if len(m.capabilities[cap.Name]) == 0 {
			delete(m.capabilities, cap.Name)
		}
	}

	delete(m.services, id)
	return nil
}

// Discover finds services based on filter (implements Discovery interface)
func (m *MockDiscovery) Discover(ctx context.Context, filter DiscoveryFilter) ([]*ServiceInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []*ServiceInfo

	for _, service := range m.services {
		// Filter by type
		if filter.Type != "" && service.Type != filter.Type {
			continue
		}

		// Filter by name
		if filter.Name != "" && service.Name != filter.Name {
			continue
		}

		// Filter by capabilities
		if len(filter.Capabilities) > 0 {
			hasCapability := false
			for _, requiredCap := range filter.Capabilities {
				for _, serviceCap := range service.Capabilities {
					if serviceCap.Name == requiredCap {
						hasCapability = true
						break
					}
				}
				if hasCapability {
					break
				}
			}
			if !hasCapability {
				continue
			}
		}

		// Filter by metadata
		if len(filter.Metadata) > 0 {
			match := true
			for k, v := range filter.Metadata {
				if service.Metadata[k] != v {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}

		// Service matches all filters
		results = append(results, service)
	}

	return results, nil
}

// FindService finds services by name (backward compatibility)
func (m *MockDiscovery) FindService(ctx context.Context, serviceName string) ([]*ServiceInfo, error) {
	return m.Discover(ctx, DiscoveryFilter{Name: serviceName})
}

// FindByCapability finds services by capability (backward compatibility)
func (m *MockDiscovery) FindByCapability(ctx context.Context, capability string) ([]*ServiceInfo, error) {
	return m.Discover(ctx, DiscoveryFilter{Capabilities: []string{capability}})
}

// Helper functions

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func removeString(slice []string, item string) []string {
	var result []string
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}
