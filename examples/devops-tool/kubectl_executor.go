package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// blockedSubcommands are kubectl subcommands that are never allowed.
// Only destructive commands that could cause data loss are blocked.
var blockedSubcommands = map[string]bool{
	"delete": true,
}

// executeKubectl runs a kubectl command with the given arguments and timeout.
// It uses exec.CommandContext to enforce timeout and captures stdout/stderr.
func executeKubectl(ctx context.Context, args []string, timeoutSeconds int) *KubectlResponse {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	if timeoutSeconds > 120 {
		timeoutSeconds = 120
	}

	fullCommand := "kubectl " + strings.Join(args, " ")

	// Add span event for kubectl execution start (request_id FIRST per Pattern 6)
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	telemetry.AddSpanEvent(ctx, "calling_kubectl",
		attribute.String("request_id", requestID),
		attribute.String("kubectl.command", fullCommand),
		attribute.Int("kubectl.timeout_seconds", timeoutSeconds),
		attribute.Int("kubectl.args_count", len(args)),
	)

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "kubectl", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdStartTime := time.Now()
	err := cmd.Run()
	cmdDuration := time.Since(cmdStartTime)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if execCtx.Err() == context.DeadlineExceeded {
			exitCode = 124 // Standard timeout exit code
			stderr.WriteString(fmt.Sprintf("\nkubectl command timed out after %d seconds", timeoutSeconds))
		} else {
			exitCode = 1
			if stderr.Len() == 0 {
				stderr.WriteString(err.Error())
			}
		}
	}

	// Record kubectl execution duration metric
	telemetry.Histogram("devops.kubectl.duration_ms",
		float64(cmdDuration.Milliseconds()),
		"module", "devops-tool",
		"subcommand", extractSubcommand(args),
	)

	status := "success"
	if exitCode != 0 {
		status = "error"
	}
	telemetry.Counter("devops.kubectl.total",
		"module", "devops-tool",
		"subcommand", extractSubcommand(args),
		"status", status,
	)

	return &KubectlResponse{
		Command:    fullCommand,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		DurationMs: cmdDuration.Milliseconds(),
	}
}

// validateKubectlArgs checks if the kubectl arguments are allowed.
// Returns an error if the command is blocked.
func validateKubectlArgs(args string) error {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return fmt.Errorf("empty kubectl arguments")
	}

	subcommand := strings.ToLower(parts[0])
	if blockedSubcommands[subcommand] {
		return fmt.Errorf("kubectl %s is not allowed — delete operations are blocked to prevent data loss", subcommand)
	}

	return nil
}

// extractSubcommand returns the first argument (kubectl subcommand) for metrics labeling.
func extractSubcommand(args []string) string {
	if len(args) == 0 {
		return "unknown"
	}
	return strings.ToLower(args[0])
}
