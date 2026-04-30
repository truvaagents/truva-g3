module github.com/truvaagents/truva-g3/examples/my-async-agent

go 1.26.2

require (
	github.com/truvaagents/truva-g3/ai v0.9.1
	github.com/truvaagents/truva-g3/core v0.9.1
	github.com/truvaagents/truva-g3/orchestration v0.9.1
	github.com/truvaagents/truva-g3/telemetry v0.9.1
)

// Use local workspace modules for development
replace (
	github.com/truvaagents/truva-g3/ai => ../../ai
	github.com/truvaagents/truva-g3/core => ../../core
	github.com/truvaagents/truva-g3/orchestration => ../../orchestration
	github.com/truvaagents/truva-g3/telemetry => ../../telemetry
)

// Run `go mod tidy` after writing your agent's Go code.
// Tidy will populate the indirect dependencies automatically.
