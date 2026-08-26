module github.com/truvaagents/truva-g3/examples/my-streaming-agent

go 1.27.0

require (
	github.com/truvaagents/truva-g3/ai v0.3.0
	github.com/truvaagents/truva-g3/core v0.3.0
	github.com/truvaagents/truva-g3/orchestration v0.3.0
	github.com/truvaagents/truva-g3/telemetry v0.3.0
)

// Use local workspace modules for development
replace (
	github.com/truvaagents/truva-g3/ai => ../../ai
	github.com/truvaagents/truva-g3/core => ../../core
	github.com/truvaagents/truva-g3/orchestration => ../../orchestration
	github.com/truvaagents/truva-g3/telemetry => ../../telemetry
)

// Run `go mod tidy` after writing your agent's Go code.
// Tidy will populate the indirect dependencies (otel, redis, etc.) automatically.
