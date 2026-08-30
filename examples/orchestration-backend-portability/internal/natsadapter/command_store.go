// Package natsadapter contains proof-only NATS adapters used by the
// orchestration backend portability example. They intentionally remain
// internal rather than becoming advertised framework providers.
package natsadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/truvaagents/truva-g3/orchestration"
)

// CommandStore uses non-durable core NATS delivery because CommandStore only
// promises to wake an active cross-instance subscriber; durable state remains
// in the independently composed checkpoint persistence backend.
type CommandStore struct {
	nc     *nats.Conn
	prefix string

	mu            sync.Mutex
	closed        bool
	nextID        uint64
	subscriptions map[uint64]context.CancelFunc
}

func NewCommandStore(nc *nats.Conn, namespace string) (*CommandStore, error) {
	prefix, err := subjectPrefix(namespace)
	if err != nil {
		return nil, err
	}
	if nc == nil || nc.IsClosed() {
		return nil, fmt.Errorf("nats adapter: active connection is required")
	}
	return &CommandStore{
		nc:            nc,
		prefix:        prefix,
		subscriptions: make(map[uint64]context.CancelFunc),
	}, nil
}

func (s *CommandStore) PublishCommand(ctx context.Context, command *orchestration.Command) error {
	if command == nil || strings.TrimSpace(command.CheckpointID) == "" {
		return fmt.Errorf("nats adapter: command with a checkpoint ID is required")
	}
	if err := s.ensureOpen(); err != nil {
		return err
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("nats adapter: marshal command: %w", err)
	}
	if err := s.nc.Publish(s.commandSubject(command.CheckpointID), payload); err != nil {
		return fmt.Errorf("nats adapter: publish command: %w", err)
	}
	if err := flushWithContext(ctx, s.nc); err != nil {
		return fmt.Errorf("nats adapter: flush command: %w", err)
	}
	return nil
}

func (s *CommandStore) SubscribeCommand(
	ctx context.Context,
	checkpointID string,
) (<-chan *orchestration.Command, func(), error) {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil, nil, fmt.Errorf("nats adapter: checkpoint ID is required")
	}
	if err := s.ensureOpen(); err != nil {
		return nil, nil, err
	}

	subscriptionContext, cancelContext := context.WithCancel(ctx)
	wireMessages := make(chan *nats.Msg, 8)
	subscription, err := s.nc.ChanSubscribe(s.commandSubject(checkpointID), wireMessages)
	if err != nil {
		cancelContext()
		return nil, nil, fmt.Errorf("nats adapter: subscribe to command: %w", err)
	}
	if err := flushWithContext(ctx, s.nc); err != nil {
		cancelContext()
		_ = subscription.Unsubscribe()
		return nil, nil, fmt.Errorf("nats adapter: activate command subscription: %w", err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancelContext()
		_ = subscription.Unsubscribe()
		return nil, nil, fmt.Errorf("nats adapter: command store is closed")
	}
	s.nextID++
	subscriptionID := s.nextID
	s.subscriptions[subscriptionID] = cancelContext
	s.mu.Unlock()

	commands := make(chan *orchestration.Command, 1)
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancelContext()
			_ = subscription.Unsubscribe()
		})
	}

	go func() {
		defer close(commands)
		defer func() {
			s.mu.Lock()
			delete(s.subscriptions, subscriptionID)
			s.mu.Unlock()
		}()
		defer stop()
		for {
			select {
			case <-subscriptionContext.Done():
				return
			case message := <-wireMessages:
				if message == nil {
					continue
				}
				var command orchestration.Command
				if err := json.Unmarshal(message.Data, &command); err != nil || command.CheckpointID != checkpointID {
					continue
				}
				select {
				case commands <- &command:
				case <-subscriptionContext.Done():
					return
				}
			}
		}
	}()
	return commands, stop, nil
}

// Close releases subscriptions owned by this store. The NATS connection is
// application-owned and deliberately remains open.
func (s *CommandStore) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancellations := make([]context.CancelFunc, 0, len(s.subscriptions))
	for _, cancel := range s.subscriptions {
		cancellations = append(cancellations, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
	return nil
}

func (s *CommandStore) ensureOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.nc.IsClosed() {
		return fmt.Errorf("nats adapter: command store is closed")
	}
	return nil
}

func (s *CommandStore) commandSubject(checkpointID string) string {
	return s.prefix + ".commands." + token(checkpointID)
}

func subjectPrefix(namespace string) (string, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return "", fmt.Errorf("nats adapter: namespace is required")
	}
	return "truvag3.portability." + token(namespace), nil
}

func token(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:12])
}

func flushWithContext(ctx context.Context, nc *nats.Conn) error {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return nc.FlushWithContext(ctx)
	}
	boundedContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return nc.FlushWithContext(boundedContext)
}

var _ orchestration.CommandStore = (*CommandStore)(nil)
