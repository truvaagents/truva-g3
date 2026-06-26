module github.com/truvaagents/truva-g3/examples/my-tool

go 1.26.4

require (
	github.com/truvaagents/truva-g3/core v0.2.0
	github.com/truvaagents/truva-g3/telemetry v0.2.0
)

// Use local workspace modules for development
replace (
	github.com/truvaagents/truva-g3/core => ../../core
	github.com/truvaagents/truva-g3/telemetry => ../../telemetry
)

// Run `go mod tidy` after writing your tool's Go code.
// Tidy will populate the indirect dependencies (otel, redis, etc.) automatically.
