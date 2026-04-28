package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// DevOpsTool provides Kubernetes cluster management capabilities via kubectl.
// It executes kubectl commands in-cluster using the pod's ServiceAccount credentials.
type DevOpsTool struct {
	*core.BaseTool
}

// --- Request Types ---

// GetClusterStatusRequest represents the input for get_cluster_status
type GetClusterStatusRequest struct {
	IncludeNodes *bool `json:"include_nodes,omitempty"` // Optional: include node details (default true)
}

// GetPodsRequest represents the input for get_pods
type GetPodsRequest struct {
	Namespace    string `json:"namespace,omitempty"`     // Optional: namespace to list pods from (default: all)
	LabelFilter  string `json:"label_filter,omitempty"`  // Optional: label selector (e.g. "app=myapp")
	FieldFilter  string `json:"field_filter,omitempty"`  // Optional: field selector (e.g. "status.phase=Running")
	OutputFormat string `json:"output_format,omitempty"` // Optional: "wide", "json", "yaml" (default: wide)
}

// GetPodLogsRequest represents the input for get_pod_logs
type GetPodLogsRequest struct {
	PodName   string `json:"pod_name"`             // Required: name of the pod
	Namespace string `json:"namespace,omitempty"`   // Optional: namespace (default: truvag3-examples)
	Container string `json:"container,omitempty"`   // Optional: container name (for multi-container pods)
	TailLines int    `json:"tail_lines,omitempty"`  // Optional: number of lines from end (default: 100)
	Previous  bool   `json:"previous,omitempty"`    // Optional: get logs from previous container instance
}

// DescribeResourceRequest represents the input for describe_resource
type DescribeResourceRequest struct {
	ResourceType string `json:"resource_type"`        // Required: pod, deployment, service, node, configmap, secret, etc.
	ResourceName string `json:"resource_name"`        // Required: name of the resource
	Namespace    string `json:"namespace,omitempty"`   // Optional: namespace (default: truvag3-examples)
}

// ScaleDeploymentRequest represents the input for scale_deployment
type ScaleDeploymentRequest struct {
	DeploymentName string `json:"deployment_name"` // Required: name of the deployment
	Replicas       int    `json:"replicas"`        // Required: desired replica count
	Namespace      string `json:"namespace,omitempty"` // Optional: namespace (default: truvag3-examples)
}

// RolloutRestartRequest represents the input for rollout_restart
type RolloutRestartRequest struct {
	DeploymentName string `json:"deployment_name"`     // Required: name of the deployment
	Namespace      string `json:"namespace,omitempty"` // Optional: namespace (default: truvag3-examples)
}

// KubectlCommandRequest represents the input for kubectl_command
type KubectlCommandRequest struct {
	Args      string `json:"args"`                // Required: kubectl arguments (e.g. "get nodes -o wide")
	Timeout   int    `json:"timeout,omitempty"`   // Optional: timeout in seconds (default 30, max 120)
	Namespace string `json:"namespace,omitempty"` // Optional: override --namespace flag
}

// --- Response Types ---

// KubectlResponse is the common response type for all kubectl-based capabilities
type KubectlResponse struct {
	Command    string `json:"command"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

// Error codes
const (
	ErrCodeInvalidRequest    = "INVALID_REQUEST"
	ErrCodeMissingField      = "MISSING_FIELD"
	ErrCodeKubectlError      = "KUBECTL_ERROR"
	ErrCodeTimeout           = "COMMAND_TIMEOUT"
	ErrCodeForbiddenCommand  = "FORBIDDEN_COMMAND"
)

// Default namespace for operations
const defaultNamespace = "truvag3-examples"

// NewDevOpsTool creates a new DevOps tool
func NewDevOpsTool() *DevOpsTool {
	tool := &DevOpsTool{
		BaseTool: core.NewTool("devops-tool"),
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all DevOps capabilities
func (d *DevOpsTool) registerCapabilities() {

	// Capability 1: Get Cluster Status
	d.RegisterCapability(core.Capability{
		Name:        "get_cluster_status",
		Description: "Gets the overall Kubernetes cluster status including node health, resource usage, and component status. Returns cluster info, node list with conditions, and system pod status.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     d.handleGetClusterStatus,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "include_nodes",
					Type:        "boolean",
					Example:     "true",
					Description: "Include detailed node information (default: true)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The kubectl command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "245"},
			},
		},
	})

	// Capability 2: Get Pods
	d.RegisterCapability(core.Capability{
		Name:        "get_pods",
		Description: "Lists pods in the cluster with status, restarts, age, and node placement. Supports filtering by namespace, labels, and field selectors.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     d.handleGetPods,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "namespace",
					Type:        "string",
					Example:     "truvag3-examples",
					Description: "Namespace to list pods from (default: all namespaces)",
				},
				{
					Name:        "label_filter",
					Type:        "string",
					Example:     "app=myapp",
					Description: "Kubernetes label selector to filter pods",
				},
				{
					Name:        "field_filter",
					Type:        "string",
					Example:     "status.phase=Running",
					Description: "Kubernetes field selector to filter pods",
				},
				{
					Name:        "output_format",
					Type:        "string",
					Example:     "wide",
					Description: "Output format: wide, json, or yaml (default: wide)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The kubectl command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "245"},
			},
		},
	})

	// Capability 3: Get Pod Logs
	d.RegisterCapability(core.Capability{
		Name:        "get_pod_logs",
		Description: "Retrieves logs from a specific pod. Supports tail lines, container selection for multi-container pods, and previous container logs.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     d.handleGetPodLogs,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "pod_name",
					Type:        "string",
					Example:     "weather-tool-abc123",
					Description: "Name of the pod to get logs from",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "namespace",
					Type:        "string",
					Example:     "truvag3-examples",
					Description: "Namespace of the pod (default: truvag3-examples)",
				},
				{
					Name:        "container",
					Type:        "string",
					Example:     "main",
					Description: "Container name for multi-container pods",
				},
				{
					Name:        "tail_lines",
					Type:        "number",
					Example:     "100",
					Description: "Number of log lines from end (default: 100, max: 1000)",
				},
				{
					Name:        "previous",
					Type:        "boolean",
					Example:     "false",
					Description: "Get logs from previous container instance (default: false)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The kubectl command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "245"},
			},
		},
	})

	// Capability 4: Describe Resource
	d.RegisterCapability(core.Capability{
		Name:        "describe_resource",
		Description: "Describes a Kubernetes resource showing detailed information including events, conditions, and configuration.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     d.handleDescribeResource,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "resource_type",
					Type:        "string",
					Example:     "deployment",
					Description: "Kubernetes resource type: pod, deployment, service, node, configmap, ingress, etc.",
				},
				{
					Name:        "resource_name",
					Type:        "string",
					Example:     "weather-tool",
					Description: "Name of the Kubernetes resource",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "namespace",
					Type:        "string",
					Example:     "truvag3-examples",
					Description: "Namespace of the resource (default: truvag3-examples)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The kubectl command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "245"},
			},
		},
	})

	// Capability 5: Scale Deployment
	d.RegisterCapability(core.Capability{
		Name:        "scale_deployment",
		Description: "Scales a Kubernetes deployment to the specified number of replicas. Returns the scale command result.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     d.handleScaleDeployment,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "deployment_name",
					Type:        "string",
					Example:     "weather-tool",
					Description: "Name of the deployment to scale",
				},
				{
					Name:        "replicas",
					Type:        "number",
					Example:     "3",
					Description: "Desired number of replicas (0-10)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "namespace",
					Type:        "string",
					Example:     "truvag3-examples",
					Description: "Namespace of the deployment (default: truvag3-examples)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The kubectl command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "245"},
			},
		},
	})

	// Capability 6: Rollout Restart
	d.RegisterCapability(core.Capability{
		Name:        "rollout_restart",
		Description: "Performs a rolling restart of a deployment. Pods are recreated one by one to pick up new configuration or image changes.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     d.handleRolloutRestart,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "deployment_name",
					Type:        "string",
					Example:     "weather-tool",
					Description: "Name of the deployment to restart",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "namespace",
					Type:        "string",
					Example:     "truvag3-examples",
					Description: "Namespace of the deployment (default: truvag3-examples)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The kubectl command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "245"},
			},
		},
	})

	// Capability 7: Kubectl Command
	d.RegisterCapability(core.Capability{
		Name: "kubectl_command",
		Description: "Executes an arbitrary kubectl command and returns the output. " +
			"The provided args are passed directly to kubectl, do NOT include 'kubectl' prefix. " +
			"Examples: 'get nodes -o wide', 'top pods -n truvag3-examples', 'get events --sort-by=.lastTimestamp'.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     d.handleKubectlCommand,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "args",
					Type:        "string",
					Example:     "get nodes -o wide",
					Description: "Arguments to pass to kubectl (do NOT include 'kubectl' prefix)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "timeout",
					Type:        "number",
					Example:     "30",
					Description: "Execution timeout in seconds (default 30, max 120)",
				},
				{
					Name:        "namespace",
					Type:        "string",
					Example:     "truvag3-examples",
					Description: "Override namespace (added as --namespace flag)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The kubectl command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "245"},
			},
		},
	})
}
