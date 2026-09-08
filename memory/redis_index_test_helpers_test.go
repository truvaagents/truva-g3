package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

type memoryScanPage struct {
	members []string
	cursor  uint64
}

type scriptedMemorySScanHook struct {
	pages []memoryScanPage
	next  int
}

func (*scriptedMemorySScanHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *scriptedMemorySScanHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() != "sscan" || hook.next >= len(hook.pages) {
			return next(ctx, command)
		}
		page := hook.pages[hook.next]
		hook.next++
		scan, ok := command.(*redis.ScanCmd)
		if !ok {
			return fmt.Errorf("SSCAN command has type %T", command)
		}
		scan.SetVal(page.members, page.cursor)
		return nil
	}
}

func (*scriptedMemorySScanHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

type memoryFailureHook struct {
	command            string
	failPipeline       bool
	failPipelineResult string
}

func (*memoryFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *memoryFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() == hook.command {
			return errors.New("injected Redis command failure")
		}
		return next(ctx, command)
	}
}

func (hook *memoryFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		if hook.failPipeline {
			return errors.New("injected Redis pipeline failure")
		}
		if err := next(ctx, commands); err != nil {
			return err
		}
		for _, command := range commands {
			if command.Name() == hook.failPipelineResult {
				command.SetErr(errors.New("injected Redis result failure"))
			}
		}
		return nil
	}
}

type memoryCaptureLogger struct {
	mu      sync.Mutex
	entries []map[string]interface{}
}

func (*memoryCaptureLogger) Info(string, map[string]interface{})  {}
func (*memoryCaptureLogger) Error(string, map[string]interface{}) {}
func (*memoryCaptureLogger) Debug(string, map[string]interface{}) {}
func (*memoryCaptureLogger) Warn(string, map[string]interface{})  {}

func (*memoryCaptureLogger) InfoWithContext(context.Context, string, map[string]interface{})  {}
func (*memoryCaptureLogger) ErrorWithContext(context.Context, string, map[string]interface{}) {}
func (*memoryCaptureLogger) DebugWithContext(context.Context, string, map[string]interface{}) {}

func (logger *memoryCaptureLogger) WarnWithContext(_ context.Context, _ string, fields map[string]interface{}) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	copyFields := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		copyFields[key] = value
	}
	logger.entries = append(logger.entries, copyFields)
}

func (logger *memoryCaptureLogger) hasOperation(wanted string) bool {
	return logger.fieldsForOperation(wanted) != nil
}

func (logger *memoryCaptureLogger) fieldsForOperation(wanted string) map[string]interface{} {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	for _, entry := range logger.entries {
		if entry["operation"] == wanted {
			return entry
		}
	}
	return nil
}
