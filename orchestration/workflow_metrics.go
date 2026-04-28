package orchestration

import (
	"sync"
	"time"
)

// WorkflowMetrics tracks workflow execution metrics
type WorkflowMetrics struct {
	mu          sync.RWMutex
	executions  int64
	successful  int64
	failed      int64
	totalTime   time.Duration
	stepMetrics map[string]*StepMetrics
}

// StepMetrics tracks metrics for individual steps
type StepMetrics struct {
	Executions  int64
	Successful  int64
	Failed      int64
	TotalTime   time.Duration
	AverageTime time.Duration
	MinTime     time.Duration
	MaxTime     time.Duration
}

// NewWorkflowMetrics creates a new metrics tracker
func NewWorkflowMetrics() *WorkflowMetrics {
	return &WorkflowMetrics{
		stepMetrics: make(map[string]*StepMetrics),
	}
}

// RecordExecution records metrics for a workflow execution
func (m *WorkflowMetrics) RecordExecution(execution *WorkflowExecution) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.executions++

	switch execution.Status {
	case ExecutionCompleted:
		m.successful++
	case ExecutionFailed:
		m.failed++
	}

	if execution.EndTime != nil {
		duration := execution.EndTime.Sub(execution.StartTime)
		m.totalTime += duration
	}

	// Record step metrics
	for stepID, step := range execution.Steps {
		if _, exists := m.stepMetrics[stepID]; !exists {
			m.stepMetrics[stepID] = &StepMetrics{
				MinTime: time.Hour * 24 * 365, // Start with a very high value
			}
		}

		metrics := m.stepMetrics[stepID]
		metrics.Executions++

		switch step.Status {
		case StepCompleted:
			metrics.Successful++
		case StepFailed:
			metrics.Failed++
		}

		if step.StartTime != nil && step.EndTime != nil {
			duration := step.EndTime.Sub(*step.StartTime)
			metrics.TotalTime += duration

			if duration < metrics.MinTime {
				metrics.MinTime = duration
			}
			if duration > metrics.MaxTime {
				metrics.MaxTime = duration
			}

			metrics.AverageTime = metrics.TotalTime / time.Duration(metrics.Executions)
		}
	}
}

// GetMetrics returns current metrics
func (m *WorkflowMetrics) GetMetrics() WorkflowMetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := WorkflowMetricsSnapshot{
		TotalExecutions: m.executions,
		Successful:      m.successful,
		Failed:          m.failed,
		SuccessRate:     0,
		AverageTime:     0,
		StepMetrics:     make(map[string]StepMetricsSnapshot),
	}

	if m.executions > 0 {
		snapshot.SuccessRate = float64(m.successful) / float64(m.executions)
		snapshot.AverageTime = m.totalTime / time.Duration(m.executions)
	}

	for stepID, metrics := range m.stepMetrics {
		snapshot.StepMetrics[stepID] = StepMetricsSnapshot{
			Executions:  metrics.Executions,
			Successful:  metrics.Successful,
			Failed:      metrics.Failed,
			SuccessRate: float64(metrics.Successful) / float64(metrics.Executions),
			AverageTime: metrics.AverageTime,
			MinTime:     metrics.MinTime,
			MaxTime:     metrics.MaxTime,
		}
	}

	return snapshot
}

// WorkflowMetricsSnapshot represents a point-in-time view of metrics
type WorkflowMetricsSnapshot struct {
	TotalExecutions int64                          `json:"total_executions"`
	Successful      int64                          `json:"successful"`
	Failed          int64                          `json:"failed"`
	SuccessRate     float64                        `json:"success_rate"`
	AverageTime     time.Duration                  `json:"average_time"`
	StepMetrics     map[string]StepMetricsSnapshot `json:"step_metrics"`
}

// StepMetricsSnapshot represents step metrics at a point in time
type StepMetricsSnapshot struct {
	Executions  int64         `json:"executions"`
	Successful  int64         `json:"successful"`
	Failed      int64         `json:"failed"`
	SuccessRate float64       `json:"success_rate"`
	AverageTime time.Duration `json:"average_time"`
	MinTime     time.Duration `json:"min_time"`
	MaxTime     time.Duration `json:"max_time"`
}

// Reset clears all metrics
func (m *WorkflowMetrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.executions = 0
	m.successful = 0
	m.failed = 0
	m.totalTime = 0
	m.stepMetrics = make(map[string]*StepMetrics)
}
