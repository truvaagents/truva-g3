package natsadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/truvaagents/truva-g3/core"
)

const (
	defaultAckWait = 350 * time.Millisecond
	streamMaxAge   = time.Hour
	dedupWindow    = 10 * time.Minute
)

// TaskTransport implements the provider-neutral dispatcher and consumer as a
// JetStream work queue with explicit acknowledgement and terminal dead letters.
// The supplied NATS connection remains application-owned.
type TaskTransport struct {
	nc         *nats.Conn
	js         nats.JetStreamContext
	prefix     string
	streamName string
	ackWait    time.Duration

	mu            sync.Mutex
	closed        bool
	subscriptions map[string]*nats.Subscription
}

type taskTransportConfig struct {
	ackWait time.Duration
}

// TaskTransportOption configures proof-owned JetStream delivery behavior.
type TaskTransportOption func(*taskTransportConfig) error

// WithAckWait sets the JetStream acknowledgement lease. Live workers should
// use a lease longer than their maximum processing time; the short default is
// intentionally retained for fast abandoned-claim conformance tests.
func WithAckWait(wait time.Duration) TaskTransportOption {
	return func(config *taskTransportConfig) error {
		if wait <= 0 {
			return fmt.Errorf("nats adapter: acknowledgement wait must be positive")
		}
		config.ackWait = wait
		return nil
	}
}

func NewTaskTransport(
	ctx context.Context,
	nc *nats.Conn,
	namespace string,
	options ...TaskTransportOption,
) (*TaskTransport, error) {
	prefix, err := subjectPrefix(namespace)
	if err != nil {
		return nil, err
	}
	if nc == nil || nc.IsClosed() {
		return nil, fmt.Errorf("nats adapter: active connection is required")
	}
	config := taskTransportConfig{ackWait: defaultAckWait}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("nats adapter: task transport option %d is nil", index)
		}
		if err := option(&config); err != nil {
			return nil, err
		}
	}
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(256))
	if err != nil {
		return nil, fmt.Errorf("nats adapter: create JetStream context: %w", err)
	}
	transport := &TaskTransport{
		nc:            nc,
		js:            js,
		prefix:        prefix,
		streamName:    "PORTABILITY_" + strings.ToUpper(token(namespace)),
		ackWait:       config.ackWait,
		subscriptions: make(map[string]*nats.Subscription),
	}
	if err := transport.ensureStream(ctx); err != nil {
		return nil, err
	}
	return transport, nil
}

func (t *TaskTransport) Dispatch(ctx context.Context, queueName string, task *core.Task) error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return fmt.Errorf("nats adapter: queue name is required")
	}
	if task == nil || strings.TrimSpace(task.ID) == "" {
		return fmt.Errorf("nats adapter: task with an ID is required")
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("nats adapter: marshal task: %w", err)
	}
	message := &nats.Msg{Subject: t.taskSubject(queueName), Header: nats.Header{}, Data: payload}
	message.Header.Set(nats.MsgIdHdr, task.ID)
	ack, err := t.js.PublishMsg(message, nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("nats adapter: dispatch task: %w", err)
	}
	if ack.Duplicate {
		return fmt.Errorf("nats adapter: task %q already exists: %w", task.ID, core.ErrTaskAlreadyExists)
	}
	return nil
}

func (t *TaskTransport) Consume(ctx context.Context, queueName string) (core.TaskHandle, error) {
	if err := t.ensureOpen(); err != nil {
		return nil, err
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return nil, fmt.Errorf("nats adapter: queue name is required")
	}
	subscription, err := t.subscription(ctx, queueName)
	if err != nil {
		return nil, err
	}
	messages, err := subscription.Fetch(1, nats.Context(ctx))
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil
		}
		if errors.Is(err, nats.ErrConsumerDeleted) || errors.Is(err, nats.ErrNoResponders) {
			t.invalidateSubscription(queueName, subscription)
		}
		return nil, fmt.Errorf("nats adapter: consume task: %w", err)
	}
	if len(messages) == 0 {
		return nil, nil
	}
	var task core.Task
	if err := json.Unmarshal(messages[0].Data, &task); err != nil {
		_ = messages[0].Term(nats.Context(ctx))
		return nil, fmt.Errorf("nats adapter: decode task: %w", err)
	}
	return &taskHandle{transport: t, message: messages[0], task: &task, queueName: queueName}, nil
}

func (t *TaskTransport) DeadLetterContains(
	ctx context.Context,
	queueName string,
	taskID string,
	reason string,
) (bool, error) {
	message, err := t.js.GetLastMsg(t.streamName, t.deadLetterSubject(queueName), nats.Context(ctx))
	if err != nil {
		if errors.Is(err, nats.ErrMsgNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("nats adapter: inspect dead letter: %w", err)
	}
	var entry deadLetter
	if err := json.Unmarshal(message.Data, &entry); err != nil {
		return false, fmt.Errorf("nats adapter: decode dead letter: %w", err)
	}
	return entry.Task != nil && entry.Task.ID == taskID && entry.Reason == reason, nil
}

// RecoverAbandoned waits for JetStream's acknowledgement lease to expire and
// then consumes the redelivered claim. The abandoned handle is checked to
// ensure the fixture is observing this transport rather than manufacturing a
// replacement message.
func (t *TaskTransport) RecoverAbandoned(
	ctx context.Context,
	queueName string,
	abandoned core.TaskHandle,
) (core.TaskHandle, error) {
	handle, ok := abandoned.(*taskHandle)
	if !ok || handle.transport != t {
		return nil, fmt.Errorf("nats adapter: abandoned handle belongs to another transport")
	}
	timer := time.NewTimer(t.ackWait + 150*time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return t.Consume(ctx, queueName)
}

// Close releases pull subscriptions but never closes the application-owned
// NATS connection or deletes durable server state.
func (t *TaskTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	subscriptions := make([]*nats.Subscription, 0, len(t.subscriptions))
	for _, subscription := range t.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	t.mu.Unlock()
	var closeError error
	for _, subscription := range subscriptions {
		if err := subscription.Unsubscribe(); err != nil && closeError == nil {
			closeError = fmt.Errorf("nats adapter: close task subscription: %w", err)
		}
	}
	return closeError
}

// DeleteStream removes only this proof fixture's uniquely named stream.
func (t *TaskTransport) DeleteStream(ctx context.Context) error {
	if err := t.js.DeleteStream(t.streamName, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("nats adapter: delete task stream: %w", err)
	}
	return nil
}

func (t *TaskTransport) ensureStream(ctx context.Context) error {
	config := &nats.StreamConfig{
		Name:       t.streamName,
		Subjects:   []string{t.prefix + ".tasks.*", t.prefix + ".dead.*"},
		Retention:  nats.WorkQueuePolicy,
		Storage:    nats.FileStorage,
		MaxAge:     streamMaxAge,
		Duplicates: dedupWindow,
		Replicas:   1,
	}
	info, err := t.js.StreamInfo(t.streamName, nats.Context(ctx))
	if err == nil {
		if info.Config.Retention != nats.WorkQueuePolicy {
			return fmt.Errorf("nats adapter: existing stream %q has incompatible retention", t.streamName)
		}
		return nil
	}
	if !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("nats adapter: inspect task stream: %w", err)
	}
	if _, err := t.js.AddStream(config, nats.Context(ctx)); err != nil {
		// Multiple API/worker replicas can observe the stream as absent at the
		// same instant. Treat a concurrently created compatible stream as
		// success instead of making one replica fail startup.
		info, inspectErr := t.js.StreamInfo(t.streamName, nats.Context(ctx))
		if inspectErr == nil && info.Config.Retention == nats.WorkQueuePolicy {
			return nil
		}
		return fmt.Errorf("nats adapter: create task stream: %w", err)
	}
	return nil
}

func (t *TaskTransport) subscription(ctx context.Context, queueName string) (*nats.Subscription, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, fmt.Errorf("nats adapter: task transport is closed")
	}
	if subscription := t.subscriptions[queueName]; subscription != nil {
		return subscription, nil
	}
	durable := "CONSUMER_" + strings.ToUpper(token(queueName))
	if err := t.ensureConsumer(ctx, durable, queueName); err != nil {
		return nil, err
	}
	subscription, err := t.js.PullSubscribe(
		t.taskSubject(queueName),
		durable,
		nats.Bind(t.streamName, durable),
	)
	if err != nil {
		return nil, fmt.Errorf("nats adapter: create task consumer: %w", err)
	}
	t.subscriptions[queueName] = subscription
	return subscription, nil
}

func (t *TaskTransport) ensureConsumer(ctx context.Context, durable, queueName string) error {
	info, err := t.js.ConsumerInfo(t.streamName, durable, nats.Context(ctx))
	if err == nil {
		return t.validateConsumer(info, queueName)
	}
	if !errors.Is(err, nats.ErrConsumerNotFound) {
		return fmt.Errorf("nats adapter: inspect task consumer: %w", err)
	}
	config := &nats.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       t.ackWait,
		MaxDeliver:    20,
		FilterSubject: t.taskSubject(queueName),
		MaxAckPending: 256,
	}
	info, err = t.js.AddConsumer(t.streamName, config, nats.Context(ctx))
	if err == nil {
		return t.validateConsumer(info, queueName)
	}
	// A second worker may have created the shared durable after our lookup.
	info, inspectErr := t.js.ConsumerInfo(t.streamName, durable, nats.Context(ctx))
	if inspectErr == nil {
		return t.validateConsumer(info, queueName)
	}
	return fmt.Errorf("nats adapter: create task consumer: %w", err)
}

func (t *TaskTransport) validateConsumer(info *nats.ConsumerInfo, queueName string) error {
	if info == nil || info.Config.AckPolicy != nats.AckExplicitPolicy ||
		info.Config.FilterSubject != t.taskSubject(queueName) ||
		info.Config.AckWait != t.ackWait || info.Config.MaxDeliver != 20 ||
		info.Config.MaxAckPending != 256 {
		return fmt.Errorf("nats adapter: existing task consumer has incompatible configuration")
	}
	return nil
}

func (t *TaskTransport) invalidateSubscription(queueName string, subscription *nats.Subscription) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.subscriptions[queueName] == subscription {
		delete(t.subscriptions, queueName)
		_ = subscription.Unsubscribe()
	}
}

func (t *TaskTransport) ensureOpen() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.nc.IsClosed() {
		return fmt.Errorf("nats adapter: task transport is closed")
	}
	return nil
}

func (t *TaskTransport) taskSubject(queueName string) string {
	return t.prefix + ".tasks." + token(queueName)
}

func (t *TaskTransport) deadLetterSubject(queueName string) string {
	return t.prefix + ".dead." + token(queueName)
}

type taskHandle struct {
	transport *TaskTransport
	message   *nats.Msg
	task      *core.Task
	queueName string

	mu      sync.Mutex
	settled bool
}

func (h *taskHandle) Task() *core.Task { return h.task }

func (h *taskHandle) Ack(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("nats adapter: task handle already settled")
	}
	if err := h.message.AckSync(nats.Context(ctx)); err != nil {
		return fmt.Errorf("nats adapter: acknowledge task: %w", err)
	}
	h.settled = true
	return nil
}

func (h *taskHandle) Nack(ctx context.Context, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled {
		return fmt.Errorf("nats adapter: task handle already settled")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("nats adapter: terminal reason is required")
	}
	entry, err := json.Marshal(deadLetter{Task: h.task, Reason: reason, FailedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("nats adapter: marshal dead letter: %w", err)
	}
	if _, err := h.transport.js.Publish(
		h.transport.deadLetterSubject(h.queueName),
		entry,
		nats.Context(ctx),
	); err != nil {
		return fmt.Errorf("nats adapter: persist dead letter: %w", err)
	}
	if err := h.message.Term(nats.Context(ctx)); err != nil {
		return fmt.Errorf("nats adapter: terminate task delivery: %w", err)
	}
	h.settled = true
	return nil
}

type deadLetter struct {
	Task     *core.Task `json:"task"`
	Reason   string     `json:"reason"`
	FailedAt time.Time  `json:"failed_at"`
}

var (
	_ core.TaskDispatcher = (*TaskTransport)(nil)
	_ core.TaskConsumer   = (*TaskTransport)(nil)
	_ core.TaskHandle     = (*taskHandle)(nil)
)
