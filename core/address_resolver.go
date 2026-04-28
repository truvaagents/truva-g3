package core

import (
	"fmt"
)

// ResolveServiceAddress determines the appropriate address and port for service registration
// based on the environment (Kubernetes or local). This function provides a single source of
// truth for address resolution logic, used by both Tools and Agents.
//
// In Kubernetes environments with a Service configured:
//   - Returns the Kubernetes Service DNS name (service.namespace.svc.cluster.local)
//   - Uses the Service port for load balancing
//   - Enables proper service discovery through Kubernetes DNS
//
// In non-Kubernetes environments:
//   - Returns the configured address or defaults to localhost
//   - Uses the configured port
//
// This abstraction ensures consistent behavior across all component types while
// maintaining their architectural independence.
func ResolveServiceAddress(config *Config, logger Logger) (string, int) {
	// Validate input
	if config == nil {
		if logger != nil {
			logger.Warn("No config provided for address resolution, using defaults", nil)
		}
		return "localhost", 8080
	}

	// Check if we're in Kubernetes with a service name configured
	if config.Kubernetes.Enabled && config.Kubernetes.ServiceName != "" {
		// Determine namespace, defaulting to "default" if not specified
		namespace := config.Kubernetes.PodNamespace
		if namespace == "" {
			namespace = "default"
		}

		// Build Kubernetes Service DNS name
		// Format: <service-name>.<namespace>.svc.cluster.local
		// This is the standard Kubernetes DNS format for services
		address := fmt.Sprintf("%s.%s.svc.cluster.local",
			config.Kubernetes.ServiceName,
			namespace)

		// Use the Kubernetes service port (not the container port)
		// This enables proper load balancing through the Service
		port := config.Kubernetes.ServicePort
		if port <= 0 {
			port = 80 // Default HTTP service port
		}

		// Log the resolution details for debugging
		if logger != nil {
			logger.Info("Resolved to Kubernetes Service DNS", map[string]interface{}{
				"service_dns":    address,
				"service_port":   port,
				"service_name":   config.Kubernetes.ServiceName,
				"namespace":      namespace,
				"pod_name":       config.Kubernetes.PodName,
				"container_port": config.Port, // Actual container port for reference
			})
		}

		return address, port
	}

	// Fallback to regular address configuration for non-Kubernetes environments
	address := config.Address
	if address == "" {
		address = "localhost"
	}

	port := config.Port
	if port <= 0 {
		port = 8080 // Default port
	}

	if logger != nil {
		logger.Debug("Resolved to standard address", map[string]interface{}{
			"address": address,
			"port":    port,
		})
	}

	return address, port
}

// BuildServiceMetadata creates metadata map with Kubernetes information if available.
// This is used to enrich service registration with deployment context.
func BuildServiceMetadata(config *Config) map[string]interface{} {
	metadata := make(map[string]interface{})

	if config == nil {
		return metadata
	}

	// Always include namespace
	metadata["namespace"] = config.Namespace

	// Add Kubernetes-specific metadata if in K8s environment
	if config.Kubernetes.Enabled {
		metadata["pod_name"] = config.Kubernetes.PodName
		metadata["pod_namespace"] = config.Kubernetes.PodNamespace
		metadata["service_name"] = config.Kubernetes.ServiceName
		metadata["container_port"] = fmt.Sprintf("%d", config.Port)
		metadata["service_port"] = fmt.Sprintf("%d", config.Kubernetes.ServicePort)

		// Additional K8s context if available
		if config.Kubernetes.PodIP != "" {
			metadata["pod_ip"] = config.Kubernetes.PodIP
		}
		if config.Kubernetes.NodeName != "" {
			metadata["node_name"] = config.Kubernetes.NodeName
		}
	}

	return metadata
}
