package orchestration

import "github.com/truvaagents/truva-g3/telemetry"

func init() {
	// Declare workflow executor metrics
	telemetry.DeclareMetrics("workflow", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:   "workflow.started",
				Type:   "counter",
				Help:   "Workflows started",
				Labels: []string{"workflow_name"},
			},
			{
				Name:   "workflow.completed",
				Type:   "counter",
				Help:   "Workflows completed",
				Labels: []string{"workflow_name", "status"},
			},
			{
				Name:    "workflow.duration_ms",
				Type:    "histogram",
				Help:    "Workflow execution time in milliseconds",
				Labels:  []string{"workflow_name", "status"},
				Unit:    "ms",
				Buckets: []float64{10, 100, 1000, 10000, 60000},
			},
			{
				Name:    "workflow.step.duration_ms",
				Type:    "histogram",
				Help:    "Individual step duration in milliseconds",
				Labels:  []string{"workflow_name", "step_name", "status"},
				Unit:    "ms",
				Buckets: []float64{1, 10, 100, 1000, 10000},
			},
			{
				Name:   "workflow.step.failures",
				Type:   "counter",
				Help:   "Step failures",
				Labels: []string{"workflow_name", "step_name", "error_type"},
			},
			{
				Name:   "workflow.active",
				Type:   "gauge",
				Help:   "Currently active workflows",
				Labels: []string{"workflow_name"},
			},
			{
				Name:   "workflow.queue.size",
				Type:   "gauge",
				Help:   "Number of workflows in queue",
				Labels: []string{"workflow_name"},
			},
		},
	})

	// Declare pipeline executor metrics
	telemetry.DeclareMetrics("pipeline", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:   "pipeline.executions",
				Type:   "counter",
				Help:   "Pipeline executions",
				Labels: []string{"pipeline_name"},
			},
			{
				Name:    "pipeline.stage.duration_ms",
				Type:    "histogram",
				Help:    "Pipeline stage duration",
				Labels:  []string{"pipeline_name", "stage_name", "status"},
				Unit:    "ms",
				Buckets: []float64{10, 100, 1000, 10000},
			},
			{
				Name:   "pipeline.stage.failures",
				Type:   "counter",
				Help:   "Pipeline stage failures",
				Labels: []string{"pipeline_name", "stage_name", "error_type"},
			},
			{
				Name:   "pipeline.throughput",
				Type:   "gauge",
				Help:   "Pipeline throughput (items/sec)",
				Labels: []string{"pipeline_name"},
			},
		},
	})

	// Declare task executor metrics
	telemetry.DeclareMetrics("executor", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:   "executor.tasks.submitted",
				Type:   "counter",
				Help:   "Tasks submitted to executor",
				Labels: []string{"executor_name", "priority"},
			},
			{
				Name:   "executor.tasks.completed",
				Type:   "counter",
				Help:   "Tasks completed by executor",
				Labels: []string{"executor_name", "status"},
			},
			{
				Name:   "executor.queue.depth",
				Type:   "gauge",
				Help:   "Current queue depth",
				Labels: []string{"executor_name"},
			},
			{
				Name:   "executor.workers.active",
				Type:   "gauge",
				Help:   "Active worker count",
				Labels: []string{"executor_name"},
			},
			{
				Name:    "executor.task.wait_ms",
				Type:    "histogram",
				Help:    "Time spent waiting in queue",
				Labels:  []string{"executor_name", "priority"},
				Unit:    "ms",
				Buckets: []float64{1, 10, 100, 1000, 10000},
			},
			{
				Name:    "executor.task.duration_ms",
				Type:    "histogram",
				Help:    "Task execution duration",
				Labels:  []string{"executor_name", "task_type"},
				Unit:    "ms",
				Buckets: []float64{1, 10, 100, 1000, 10000},
			},
		},
	})

	// Declare result data management metrics
	telemetry.DeclareMetrics("result_management", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{
				Name:   "orchestration.result_trim.triggered",
				Type:   "counter",
				Help:   "Step results that required trimming",
				Labels: []string{"agent_name"},
			},
			{
				Name:    "orchestration.result.original_size_bytes",
				Type:    "histogram",
				Help:    "Original step result sizes in bytes",
				Labels:  []string{"agent_name"},
				Unit:    "bytes",
				Buckets: []float64{1024, 4096, 8192, 32768, 65536, 131072, 262144},
			},
			{
				Name:    "orchestration.result.trimmed_size_bytes",
				Type:    "histogram",
				Help:    "Trimmed step result sizes in bytes",
				Labels:  []string{"agent_name"},
				Unit:    "bytes",
				Buckets: []float64{1024, 2048, 4096, 8192, 16384, 32768},
			},
			{
				Name:    "orchestration.synthesis_prompt.size_bytes",
				Type:    "histogram",
				Help:    "Synthesis prompt size after trimming",
				Labels:  []string{},
				Unit:    "bytes",
				Buckets: []float64{4096, 8192, 16384, 32768, 65536, 131072},
			},
			{
				Name:   "orchestration.result_distill.triggered",
				Type:   "counter",
				Help:   "LLM distillation activations",
				Labels: []string{"agent_name"},
			},
			{
				Name:   "orchestration.result_distill.failed",
				Type:   "counter",
				Help:   "LLM distillation failures (fell back to structural trim)",
				Labels: []string{"agent_name"},
			},
			{
				Name:    "orchestration.result_distill.duration_ms",
				Type:    "histogram",
				Help:    "LLM distillation latency",
				Labels:  []string{"agent_name"},
				Unit:    "ms",
				Buckets: []float64{50, 100, 200, 500, 1000, 2000, 5000},
			},
			{
				Name:   "orchestration.result_trim.micro_resolution",
				Type:   "counter",
				Help:   "Micro-resolution source data trims (Phase 5)",
				Labels: []string{"capability"},
			},
			{
				Name:   "orchestration.result_trim.agent_input",
				Type:   "counter",
				Help:   "Agent input parameter trims (Phase 8)",
				Labels: []string{"agent_name"},
			},
		},
	})
}
