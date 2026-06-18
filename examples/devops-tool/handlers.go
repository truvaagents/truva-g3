package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendError sends a structured error response using core.ToolResponse.
// CRITICAL: WriteHeader must be called before Encode per TOOL_DEVELOPMENT_GUIDE Section 6.
func sendError(w http.ResponseWriter, code string, message string, httpStatus int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "TIMEOUT"),
		},
	})
}

// extractHandlerContext sets up tracing and logging boilerplate for a handler.
// Returns the enriched context (with request_id in baggage for downstream access),
// the request_id, and the start time.
func (d *DevOpsTool) extractHandlerContext(w http.ResponseWriter, r *http.Request, capability string) (context.Context, string, time.Time) {
	ctx := r.Context()
	startTime := time.Now()

	// 1. Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation, with header fallbacks per TOOL_DEVELOPMENT_GUIDE §12
	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = r.Header.Get("X-TruvaG3-Request-ID")
	}
	if requestID == "" {
		requestID = r.Header.Get("X-Request-ID")
	}
	if requestID == "" {
		requestID = uuid.New().String()
	}

	// 3. Propagate request_id via context baggage for downstream access (Pattern 3)
	ctx = telemetry.WithBaggage(ctx, "request_id", requestID)

	// 4. Set span attributes (request_id FIRST per tracing guide)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "devops-tool"),
		attribute.String("truvag3.capability", capability),
	)

	// 5. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", capability),
	)

	// 6. Log request start
	if d.Logger != nil {
		d.Logger.InfoWithContext(ctx, fmt.Sprintf("Processing %s request", capability), map[string]interface{}{
			"operation":  capability,
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	return ctx, requestID, startTime
}

// recordCompletion records metrics and logs completion with the correct status.
// Pass kubectlExitCode=0 for success, non-zero for kubectl failure.
func (d *DevOpsTool) recordCompletion(r *http.Request, capability, requestID string, startTime time.Time, kubectlExitCode int, extraAttrs ...attribute.KeyValue) {
	ctx := r.Context()
	duration := time.Since(startTime)

	status := "success"
	if kubectlExitCode != 0 {
		status = "error"
	}

	telemetry.Counter("devops.requests.total",
		"module", "devops-tool",
		"capability", capability,
		"status", status,
	)
	telemetry.Histogram("devops.request.duration_ms",
		float64(duration.Milliseconds()),
		"module", "devops-tool",
		"capability", capability,
	)
	telemetry.RecordToolCall("devops-tool", capability, float64(duration.Milliseconds()), status)

	attrs := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.Int64("duration_ms", duration.Milliseconds()),
		attribute.Int("exit_code", kubectlExitCode),
	}
	attrs = append(attrs, extraAttrs...)
	telemetry.AddSpanEvent(ctx, capability+"_completed", attrs...)

	if d.Logger != nil {
		d.Logger.InfoWithContext(ctx, capability+" completed", map[string]interface{}{
			"operation":   capability,
			"status":      status,
			"exit_code":   kubectlExitCode,
			"duration_ms": duration.Milliseconds(),
			"request_id":  requestID,
		})
	}
}

// handleGetClusterStatus returns cluster-wide status information.
func (d *DevOpsTool) handleGetClusterStatus(w http.ResponseWriter, r *http.Request) {
	ctx, requestID, startTime := d.extractHandlerContext(w, r, "get_cluster_status")

	var req GetClusterStatusRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // Empty body OK — use defaults

	// Default include_nodes to true when not explicitly provided.
	// Using *bool distinguishes "not set" (nil → default true) from "explicitly false".
	includeNodes := req.IncludeNodes == nil || *req.IncludeNodes

	// Get cluster info
	result := executeKubectl(ctx, []string{"cluster-info"}, 15)

	// If nodes requested, append node info. Surface a failed sub-command
	// (e.g. busy/timeout under load) as a visible "unavailable" marker rather
	// than silently dropping the section — otherwise a partial report reads as
	// a healthy cluster.
	if includeNodes {
		nodeResult := executeKubectl(ctx, []string{"get", "nodes", "-o", "wide"}, 15)
		if nodeResult.ExitCode == 0 {
			result.Stdout += "\n---\nNodes:\n" + nodeResult.Stdout
		} else {
			result.Stdout += fmt.Sprintf("\n---\nNodes: unavailable (exit %d: %s)\n",
				nodeResult.ExitCode, strings.TrimSpace(nodeResult.Stderr))
		}
	}

	// Append component status (same visible-degradation handling as nodes).
	csResult := executeKubectl(ctx, []string{"get", "componentstatuses", "--no-headers"}, 10)
	if csResult.ExitCode == 0 {
		if csResult.Stdout != "" {
			result.Stdout += "\n---\nComponent Status:\n" + csResult.Stdout
		}
	} else {
		result.Stdout += fmt.Sprintf("\n---\nComponent Status: unavailable (exit %d: %s)\n",
			csResult.ExitCode, strings.TrimSpace(csResult.Stderr))
	}

	if result.ExitCode != 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("kubectl cluster-info failed: exit %d", result.ExitCode))
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "get_cluster_status",
			"error_type", "kubectl_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "kubectl cluster-info failed", map[string]interface{}{
				"operation":   "get_cluster_status",
				"exit_code":   result.ExitCode,
				"error":       result.Stderr,
				"error_type":  "kubectl_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
	}

	d.recordCompletion(r, "get_cluster_status", requestID, startTime, result.ExitCode)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// handleGetPods lists pods with optional filtering.
func (d *DevOpsTool) handleGetPods(w http.ResponseWriter, r *http.Request) {
	ctx, requestID, startTime := d.extractHandlerContext(w, r, "get_pods")

	var req GetPodsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Empty body OK — list all pods
	}

	// Build kubectl args
	args := []string{"get", "pods"}

	if req.Namespace != "" {
		args = append(args, "-n", req.Namespace)
	} else {
		args = append(args, "--all-namespaces")
	}

	if req.LabelFilter != "" {
		args = append(args, "-l", req.LabelFilter)
	}
	if req.FieldFilter != "" {
		args = append(args, "--field-selector", req.FieldFilter)
	}

	switch strings.ToLower(req.OutputFormat) {
	case "json":
		args = append(args, "-o", "json")
	case "yaml":
		args = append(args, "-o", "yaml")
	default:
		args = append(args, "-o", "wide")
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("pods.namespace", req.Namespace),
		attribute.String("pods.label_filter", req.LabelFilter),
	)

	result := executeKubectl(ctx, args, 30)

	if result.ExitCode != 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("kubectl get pods failed: exit %d", result.ExitCode))
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "get_pods",
			"error_type", "kubectl_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "kubectl get pods failed", map[string]interface{}{
				"operation":   "get_pods",
				"exit_code":   result.ExitCode,
				"error":       result.Stderr,
				"error_type":  "kubectl_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
	}

	d.recordCompletion(r, "get_pods", requestID, startTime, result.ExitCode,
		attribute.String("namespace", req.Namespace),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// handleGetPodLogs retrieves logs from a specific pod.
func (d *DevOpsTool) handleGetPodLogs(w http.ResponseWriter, r *http.Request) {
	ctx, requestID, startTime := d.extractHandlerContext(w, r, "get_pod_logs")

	var req GetPodLogsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "get_pod_logs",
			"error_type", "decode_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "get_pod_logs",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeInvalidRequest, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required field
	req.PodName = strings.TrimSpace(req.PodName)
	if req.PodName == "" {
		err := fmt.Errorf("pod_name is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "get_pod_logs",
			"error_type", "validation_error",
		)
		if d.Logger != nil {
			d.Logger.WarnWithContext(ctx, "Missing pod_name", map[string]interface{}{
				"operation":   "get_pod_logs",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeMissingField, "pod_name is required", http.StatusBadRequest)
		return
	}

	// Apply defaults
	ns := req.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	tailLines := req.TailLines
	if tailLines <= 0 {
		tailLines = 100
	}
	if tailLines > 1000 {
		tailLines = 1000
	}

	// Build kubectl args
	args := []string{"logs", req.PodName, "-n", ns, "--tail", fmt.Sprintf("%d", tailLines)}
	if req.Container != "" {
		args = append(args, "-c", req.Container)
	}
	if req.Previous {
		args = append(args, "--previous")
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("logs.pod_name", req.PodName),
		attribute.String("logs.namespace", ns),
		attribute.Int("logs.tail_lines", tailLines),
	)

	result := executeKubectl(ctx, args, 30)

	if result.ExitCode != 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("kubectl logs failed: exit %d", result.ExitCode))
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "get_pod_logs",
			"error_type", "kubectl_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "kubectl logs failed", map[string]interface{}{
				"operation":   "get_pod_logs",
				"pod_name":    req.PodName,
				"namespace":   ns,
				"exit_code":   result.ExitCode,
				"error":       result.Stderr,
				"error_type":  "kubectl_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
	}

	d.recordCompletion(r, "get_pod_logs", requestID, startTime, result.ExitCode,
		attribute.String("pod_name", req.PodName),
		attribute.String("namespace", ns),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// handleDescribeResource describes a Kubernetes resource.
func (d *DevOpsTool) handleDescribeResource(w http.ResponseWriter, r *http.Request) {
	ctx, requestID, startTime := d.extractHandlerContext(w, r, "describe_resource")

	var req DescribeResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "describe_resource",
			"error_type", "decode_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "describe_resource",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeInvalidRequest, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required fields
	req.ResourceType = strings.TrimSpace(strings.ToLower(req.ResourceType))
	req.ResourceName = strings.TrimSpace(req.ResourceName)

	if req.ResourceType == "" || req.ResourceName == "" {
		err := fmt.Errorf("resource_type and resource_name are required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "describe_resource",
			"error_type", "validation_error",
		)
		if d.Logger != nil {
			d.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
				"operation":   "describe_resource",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeMissingField, "resource_type and resource_name are required", http.StatusBadRequest)
		return
	}

	ns := req.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	// Build kubectl args
	args := []string{"describe", req.ResourceType, req.ResourceName, "-n", ns}

	// Nodes are cluster-scoped — don't use -n flag
	if req.ResourceType == "node" || req.ResourceType == "nodes" {
		args = []string{"describe", req.ResourceType, req.ResourceName}
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("describe.resource_type", req.ResourceType),
		attribute.String("describe.resource_name", req.ResourceName),
		attribute.String("describe.namespace", ns),
	)

	result := executeKubectl(ctx, args, 30)

	if result.ExitCode != 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("kubectl describe failed: exit %d", result.ExitCode))
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "describe_resource",
			"error_type", "kubectl_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "kubectl describe failed", map[string]interface{}{
				"operation":      "describe_resource",
				"resource_type":  req.ResourceType,
				"resource_name":  req.ResourceName,
				"namespace":      ns,
				"exit_code":      result.ExitCode,
				"error":          result.Stderr,
				"error_type":     "kubectl_error",
				"request_id":     requestID,
				"status":         "failure",
				"duration_ms":    time.Since(startTime).Milliseconds(),
			})
		}
	}

	d.recordCompletion(r, "describe_resource", requestID, startTime, result.ExitCode,
		attribute.String("resource_type", req.ResourceType),
		attribute.String("resource_name", req.ResourceName),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// handleScaleDeployment scales a deployment to the specified replica count.
func (d *DevOpsTool) handleScaleDeployment(w http.ResponseWriter, r *http.Request) {
	ctx, requestID, startTime := d.extractHandlerContext(w, r, "scale_deployment")

	var req ScaleDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "scale_deployment",
			"error_type", "decode_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "scale_deployment",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeInvalidRequest, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required fields
	req.DeploymentName = strings.TrimSpace(req.DeploymentName)
	if req.DeploymentName == "" {
		err := fmt.Errorf("deployment_name is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "scale_deployment",
			"error_type", "validation_error",
		)
		if d.Logger != nil {
			d.Logger.WarnWithContext(ctx, "Missing deployment_name", map[string]interface{}{
				"operation":   "scale_deployment",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeMissingField, "deployment_name is required", http.StatusBadRequest)
		return
	}

	// Validate replica count
	if req.Replicas < 0 || req.Replicas > 10 {
		err := fmt.Errorf("replicas must be between 0 and 10")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "scale_deployment",
			"error_type", "validation_error",
		)
		if d.Logger != nil {
			d.Logger.WarnWithContext(ctx, "Invalid replica count", map[string]interface{}{
				"operation":       "scale_deployment",
				"replicas":        req.Replicas,
				"error_type":      "validation_error",
				"request_id":      requestID,
				"status":          "failure",
				"duration_ms":     time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeInvalidRequest, "replicas must be between 0 and 10", http.StatusBadRequest)
		return
	}

	ns := req.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	args := []string{
		"scale", "deployment", req.DeploymentName,
		"--replicas", fmt.Sprintf("%d", req.Replicas),
		"-n", ns,
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("scale.deployment", req.DeploymentName),
		attribute.Int("scale.replicas", req.Replicas),
		attribute.String("scale.namespace", ns),
	)

	if d.Logger != nil {
		d.Logger.InfoWithContext(ctx, "Scaling deployment", map[string]interface{}{
			"operation":       "scale_deployment",
			"deployment_name": req.DeploymentName,
			"replicas":        req.Replicas,
			"namespace":       ns,
			"request_id":      requestID,
		})
	}

	result := executeKubectl(ctx, args, 30)

	if result.ExitCode != 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("kubectl scale failed: exit %d", result.ExitCode))
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "scale_deployment",
			"error_type", "kubectl_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "kubectl scale failed", map[string]interface{}{
				"operation":       "scale_deployment",
				"deployment_name": req.DeploymentName,
				"replicas":        req.Replicas,
				"namespace":       ns,
				"exit_code":       result.ExitCode,
				"error":           result.Stderr,
				"error_type":      "kubectl_error",
				"request_id":      requestID,
				"status":          "failure",
				"duration_ms":     time.Since(startTime).Milliseconds(),
			})
		}
	}

	d.recordCompletion(r, "scale_deployment", requestID, startTime, result.ExitCode,
		attribute.String("deployment", req.DeploymentName),
		attribute.Int("replicas", req.Replicas),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// handleRolloutRestart performs a rolling restart of a deployment.
func (d *DevOpsTool) handleRolloutRestart(w http.ResponseWriter, r *http.Request) {
	ctx, requestID, startTime := d.extractHandlerContext(w, r, "rollout_restart")

	var req RolloutRestartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "rollout_restart",
			"error_type", "decode_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "rollout_restart",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeInvalidRequest, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required field
	req.DeploymentName = strings.TrimSpace(req.DeploymentName)
	if req.DeploymentName == "" {
		err := fmt.Errorf("deployment_name is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "rollout_restart",
			"error_type", "validation_error",
		)
		if d.Logger != nil {
			d.Logger.WarnWithContext(ctx, "Missing deployment_name", map[string]interface{}{
				"operation":   "rollout_restart",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeMissingField, "deployment_name is required", http.StatusBadRequest)
		return
	}

	ns := req.Namespace
	if ns == "" {
		ns = defaultNamespace
	}

	args := []string{"rollout", "restart", "deployment/" + req.DeploymentName, "-n", ns}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("rollout.deployment", req.DeploymentName),
		attribute.String("rollout.namespace", ns),
	)

	if d.Logger != nil {
		d.Logger.InfoWithContext(ctx, "Restarting deployment", map[string]interface{}{
			"operation":       "rollout_restart",
			"deployment_name": req.DeploymentName,
			"namespace":       ns,
			"request_id":      requestID,
		})
	}

	result := executeKubectl(ctx, args, 30)

	if result.ExitCode != 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("kubectl rollout restart failed: exit %d", result.ExitCode))
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "rollout_restart",
			"error_type", "kubectl_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "kubectl rollout restart failed", map[string]interface{}{
				"operation":       "rollout_restart",
				"deployment_name": req.DeploymentName,
				"namespace":       ns,
				"exit_code":       result.ExitCode,
				"error":           result.Stderr,
				"error_type":      "kubectl_error",
				"request_id":      requestID,
				"status":          "failure",
				"duration_ms":     time.Since(startTime).Milliseconds(),
			})
		}
	}

	d.recordCompletion(r, "rollout_restart", requestID, startTime, result.ExitCode,
		attribute.String("deployment", req.DeploymentName),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// handleKubectlCommand executes an arbitrary kubectl command.
func (d *DevOpsTool) handleKubectlCommand(w http.ResponseWriter, r *http.Request) {
	ctx, requestID, startTime := d.extractHandlerContext(w, r, "kubectl_command")

	var req KubectlCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "kubectl_command",
			"error_type", "decode_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "kubectl_command",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeInvalidRequest, "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required field
	req.Args = strings.TrimSpace(req.Args)
	if req.Args == "" {
		err := fmt.Errorf("args is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "kubectl_command",
			"error_type", "validation_error",
		)
		if d.Logger != nil {
			d.Logger.WarnWithContext(ctx, "Empty kubectl args", map[string]interface{}{
				"operation":   "kubectl_command",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeMissingField, "args is required", http.StatusBadRequest)
		return
	}

	// Validate the command is not blocked
	if err := validateKubectlArgs(req.Args); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "kubectl_command",
			"error_type", "forbidden_command",
		)
		if d.Logger != nil {
			d.Logger.WarnWithContext(ctx, "Blocked kubectl command", map[string]interface{}{
				"operation":   "kubectl_command",
				"args":        req.Args,
				"error":       err.Error(),
				"error_type":  "forbidden_command",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, ErrCodeForbiddenCommand, err.Error(), http.StatusForbidden)
		return
	}

	// Set timeout
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 120 {
		timeout = 120
	}

	// Build args from the string
	args := strings.Fields(req.Args)

	// Inject namespace if provided and not already present
	if req.Namespace != "" {
		hasNs := false
		for _, a := range args {
			if a == "-n" || a == "--namespace" || strings.HasPrefix(a, "-n=") || strings.HasPrefix(a, "--namespace=") {
				hasNs = true
				break
			}
		}
		if !hasNs {
			args = append(args, "--namespace", req.Namespace)
		}
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("kubectl.args", req.Args),
		attribute.Int("kubectl.timeout", timeout),
	)

	if d.Logger != nil {
		d.Logger.InfoWithContext(ctx, "Executing kubectl command", map[string]interface{}{
			"operation":  "kubectl_command",
			"args":       req.Args,
			"timeout":    timeout,
			"namespace":  req.Namespace,
			"request_id": requestID,
		})
	}

	result := executeKubectl(ctx, args, timeout)

	if result.ExitCode != 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("kubectl command failed: exit %d", result.ExitCode))
		telemetry.Counter("devops.errors.total",
			"module", "devops-tool",
			"capability", "kubectl_command",
			"error_type", "kubectl_error",
		)
		if d.Logger != nil {
			d.Logger.ErrorWithContext(ctx, "kubectl command failed", map[string]interface{}{
				"operation":   "kubectl_command",
				"args":        req.Args,
				"exit_code":   result.ExitCode,
				"error":       result.Stderr,
				"error_type":  "kubectl_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
	}

	d.recordCompletion(r, "kubectl_command", requestID, startTime, result.ExitCode,
		attribute.String("args", req.Args),
	)

	// Always return 200 — the exit_code in the response indicates command success/failure
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}
