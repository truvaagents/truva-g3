package orchestration

import (
	"context"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// Mock implementations for testing

// mockCircuitBreaker implements core.CircuitBreaker for testing
type mockCircuitBreaker struct {
	executeFunc func(context.Context, func() error) error
	state       string
}

func (m *mockCircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, fn)
	}
	return fn()
}

func (m *mockCircuitBreaker) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, fn func() error) error {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, fn)
	}
	return fn()
}

func (m *mockCircuitBreaker) GetState() string {
	if m.state != "" {
		return m.state
	}
	return "closed"
}

func (m *mockCircuitBreaker) GetMetrics() map[string]interface{} {
	return map[string]interface{}{
		"success": 0,
		"failure": 0,
	}
}

func (m *mockCircuitBreaker) Reset() {
	m.state = "closed"
}

func (m *mockCircuitBreaker) CanExecute() bool {
	return m.state != "open"
}

// mockLogger implements core.Logger for testing
type mockLogger struct {
	debugFunc func(string, map[string]interface{})
	infoFunc  func(string, map[string]interface{})
	warnFunc  func(string, map[string]interface{})
	errorFunc func(string, map[string]interface{})
	messages  []string
}

func (m *mockLogger) Debug(msg string, fields map[string]interface{}) {
	m.messages = append(m.messages, msg)
	if m.debugFunc != nil {
		m.debugFunc(msg, fields)
	}
}

func (m *mockLogger) Info(msg string, fields map[string]interface{}) {
	m.messages = append(m.messages, msg)
	if m.infoFunc != nil {
		m.infoFunc(msg, fields)
	}
}

func (m *mockLogger) Warn(msg string, fields map[string]interface{}) {
	m.messages = append(m.messages, msg)
	if m.warnFunc != nil {
		m.warnFunc(msg, fields)
	}
}

func (m *mockLogger) Error(msg string, fields map[string]interface{}) {
	m.messages = append(m.messages, msg)
	if m.errorFunc != nil {
		m.errorFunc(msg, fields)
	}
}

// Context-aware logging methods
func (m *mockLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.Debug(msg, fields)
}

func (m *mockLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.Info(msg, fields)
}

func (m *mockLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.Warn(msg, fields)
}

func (m *mockLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	m.Error(msg, fields)
}

func (m *mockLogger) WithFields(fields map[string]interface{}) core.Logger {
	return m
}

func (m *mockLogger) WithError(err error) core.Logger {
	return m
}

// mockTelemetry implements core.Telemetry for testing
type mockTelemetry struct {
	spans         []string
	metrics       map[string]float64
	metricRecords []mockMetricRecord
}

type mockMetricRecord struct {
	name   string
	value  float64
	labels map[string]string
}

func (m *mockTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	if m.spans == nil {
		m.spans = []string{}
	}
	m.spans = append(m.spans, name)
	return ctx, &mockSpan{name: name}
}

func (m *mockTelemetry) RecordMetric(name string, value float64, labels map[string]string) {
	if m.metrics == nil {
		m.metrics = make(map[string]float64)
	}
	m.metrics[name] = value
	m.metricRecords = append(m.metricRecords, mockMetricRecord{
		name: name, value: value, labels: cloneStringMap(labels),
	})
}

// mockSpan implements core.Span for testing
type mockSpan struct {
	name       string
	attributes map[string]interface{}
	ended      bool
	errors     []error
}

func (s *mockSpan) End() {
	s.ended = true
}

func (s *mockSpan) SetAttribute(key string, value interface{}) {
	if s.attributes == nil {
		s.attributes = make(map[string]interface{})
	}
	s.attributes[key] = value
}

func (s *mockSpan) RecordError(err error) {
	s.errors = append(s.errors, err)
}
