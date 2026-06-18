package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

// Synthetic exit codes used when kubectl itself never produced one. These are
// tool-internal (kubectl never emits them); the capability schema documents
// them so LLM consumers don't mistake them for cluster/permission failures.
const (
	exitCodeTimeout   = 124 // command killed because its deadline was exceeded
	exitCodeCancelled = 125 // caller cancelled the request before completion
	exitCodeBusy      = 75  // EX_TEMPFAIL: no concurrency slot became available
)

// Attribution of why a context-bound operation ended, used to map a failure to
// the right exit code and message in both the slot-wait and post-exec paths.
const (
	causeNone            = ""                 // the command itself failed, not the context
	causeParentCancelled = "parent_cancelled" // caller cancelled ctx
	causeParentDeadline  = "parent_deadline"  // caller's own deadline fired
	causeOwnDeadline     = "own_deadline"     // our timeoutSeconds deadline fired
)

// contextCause attributes why work tied to execCtx ended. The parent is checked
// first because, when the caller's deadline/cancel propagates, execCtx reports
// the same condition; only when neither parent state is set does an execCtx
// DeadlineExceeded mean our own timeoutSeconds fired. Returns causeNone when
// neither context is done — i.e. the error came from the command itself.
func contextCause(parent, execCtx context.Context) string {
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return causeParentCancelled
	case errors.Is(parent.Err(), context.DeadlineExceeded):
		return causeParentDeadline
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		return causeOwnDeadline
	default:
		return causeNone
	}
}

// classifyWaitFailure maps a failed slot acquisition to an exit code and a
// human-readable reason, distinguishing caller cancellation/deadline from
// genuine pool saturation (the only case that is truly "busy").
func classifyWaitFailure(parent, execCtx context.Context, timeoutSeconds int) (int, string) {
	switch contextCause(parent, execCtx) {
	case causeParentCancelled:
		return exitCodeCancelled, "kubectl not executed: request cancelled by caller while waiting for a concurrency slot"
	case causeParentDeadline:
		return exitCodeTimeout, "kubectl not executed: caller-imposed deadline exceeded while waiting for a concurrency slot"
	default: // causeOwnDeadline: our timeoutSeconds elapsed with no slot free
		return exitCodeBusy, fmt.Sprintf("kubectl not executed: all %d concurrency slots were busy and none freed within %ds",
			cap(kubectlSem), timeoutSeconds)
	}
}

// kubectlStatusLabel maps an exit code to a metric status label so every
// outcome (success, error, timeout, cancelled, busy) is distinguishable.
func kubectlStatusLabel(exitCode int) string {
	switch exitCode {
	case 0:
		return "success"
	case exitCodeBusy:
		return "busy"
	case exitCodeTimeout:
		return "timeout"
	case exitCodeCancelled:
		return "cancelled"
	default:
		return "error"
	}
}

// recordKubectlMetrics emits the duration histogram and total counter together
// for a kubectl operation, so the two always reconcile across every outcome —
// including the slot-wait/busy path, which would otherwise be missing from the
// duration distribution. durationMs is the operation's wall time (exec time, or
// the wait time when no slot was acquired).
func recordKubectlMetrics(subcommand string, exitCode int, durationMs int64) {
	telemetry.Histogram("devops.kubectl.duration_ms",
		float64(durationMs),
		"module", "devops-tool",
		"subcommand", subcommand,
	)
	telemetry.Counter("devops.kubectl.total",
		"module", "devops-tool",
		"subcommand", subcommand,
		"status", kubectlStatusLabel(exitCode),
	)
}

// kubectlSem bounds the number of concurrent kubectl subprocesses per pod.
// Each kubectl fork peaks around ~25Mi RSS and buffers its full output in
// memory, so an unbounded fan-out (e.g. a health-check plan issuing ~10
// kubectl_command calls at once) can exceed the pod's memory limit and stall
// the cgroup, killing in-flight processes. Capping concurrency keeps peak
// memory bounded regardless of how wide the caller fans out.
var kubectlSem = make(chan struct{}, kubectlMaxConcurrency())

// kubectlMaxConcurrency returns the per-pod concurrent-exec limit from
// DEVOPS_KUBECTL_MAX_CONCURRENCY, defaulting to 3 when unset or invalid.
func kubectlMaxConcurrency() int {
	if v := os.Getenv("DEVOPS_KUBECTL_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
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

	// Acquire a concurrency slot before forking kubectl. The wait shares the
	// command's deadline, so total time (wait + exec) stays bounded by
	// timeoutSeconds. If no slot frees up in time, fail fast with a clear
	// message instead of piling on memory pressure.
	waitStart := time.Now()
	select {
	case kubectlSem <- struct{}{}:
		defer func() { <-kubectlSem }()
	case <-execCtx.Done():
		// The wait ended without a slot. Attribute the real cause: caller
		// cancellation / caller deadline vs. genuine pool saturation — don't
		// report "busy" when the caller actually went away. Record the wait
		// duration so saturation latency is visible (not a silent 0ms).
		waitMs := time.Since(waitStart).Milliseconds()
		exitCode, reason := classifyWaitFailure(ctx, execCtx, timeoutSeconds)
		recordKubectlMetrics(extractSubcommand(args), exitCode, waitMs)
		return &KubectlResponse{
			Command:    fullCommand,
			Stderr:     reason,
			ExitCode:   exitCode,
			DurationMs: waitMs,
		}
	}

	cmd := exec.CommandContext(execCtx, "kubectl", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdStartTime := time.Now()
	err := cmd.Run()
	cmdDuration := time.Since(cmdStartTime)

	exitCode := 0
	if err != nil {
		// Classify the cause BEFORE the *exec.ExitError check: a deadline- or
		// cancellation-driven kill surfaces as an *exec.ExitError ("signal:
		// killed") whose ExitCode() is -1, which would otherwise mask the real
		// reason and ship an empty error. Always leave stderr non-empty so the
		// failure is diagnosable downstream.
		switch contextCause(ctx, execCtx) {
		case causeOwnDeadline:
			exitCode = exitCodeTimeout
			writeStderrLine(&stderr, fmt.Sprintf("kubectl command timed out after %d seconds", timeoutSeconds))
		case causeParentDeadline:
			// The caller's own (shorter) deadline fired, not our timeoutSeconds —
			// don't claim a timeout duration the command never reached.
			exitCode = exitCodeTimeout
			writeStderrLine(&stderr, "kubectl command stopped: caller-imposed deadline exceeded before completion")
		case causeParentCancelled:
			exitCode = exitCodeCancelled
			writeStderrLine(&stderr, "kubectl command cancelled by caller before completion")
		default:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
				if stderr.Len() == 0 {
					// Non-zero exit (or signal kill, exit -1) with no stderr.
					writeStderrLine(&stderr, fmt.Sprintf("kubectl exited abnormally (%s) with no output", err.Error()))
				}
			} else {
				exitCode = 1
				if stderr.Len() == 0 {
					writeStderrLine(&stderr, err.Error())
				}
			}
		}
	}

	recordKubectlMetrics(extractSubcommand(args), exitCode, cmdDuration.Milliseconds())

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

// writeStderrLine appends msg to stderr, inserting a newline separator only
// when stderr already holds kubectl's own output, so a synthesized reason never
// runs into captured stderr and the buffer is never left empty on failure.
func writeStderrLine(stderr *bytes.Buffer, msg string) {
	if stderr.Len() > 0 {
		stderr.WriteString("\n")
	}
	stderr.WriteString(msg)
}

// extractSubcommand returns the first argument (kubectl subcommand) for metrics labeling.
func extractSubcommand(args []string) string {
	if len(args) == 0 {
		return "unknown"
	}
	return strings.ToLower(args[0])
}
