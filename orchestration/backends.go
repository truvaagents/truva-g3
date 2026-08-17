package orchestration

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// BackendCapability names a provider-neutral orchestration storage or
// coordination contract. It describes what a feature needs, not which backend
// supplies it.
type BackendCapability string

const (
	BackendExecutionDebug   BackendCapability = "execution_debug"
	BackendLLMDebug         BackendCapability = "llm_debug"
	BackendCheckpoints      BackendCapability = "checkpoints"
	BackendCheckpointExpiry BackendCapability = "checkpoint_expiry"
	BackendCommands         BackendCapability = "commands"
	BackendWorkflowState    BackendCapability = "workflow_state"
	BackendSchedules        BackendCapability = "schedules"
	BackendTasks            BackendCapability = "tasks"
	BackendTaskQueue        BackendCapability = "task_queue"
	BackendTaskDispatcher   BackendCapability = "task_dispatcher"
	BackendTaskConsumer     BackendCapability = "task_consumer"
	BackendLock             BackendCapability = "lock"
	BackendSkills           BackendCapability = "skills"
)

var knownBackendCapabilities = map[BackendCapability]struct{}{
	BackendExecutionDebug: {}, BackendLLMDebug: {}, BackendCheckpoints: {},
	BackendCheckpointExpiry: {}, BackendCommands: {}, BackendWorkflowState: {},
	BackendSchedules: {}, BackendTasks: {}, BackendTaskQueue: {},
	BackendTaskDispatcher: {}, BackendTaskConsumer: {}, BackendLock: {}, BackendSkills: {},
}

// BackendRequirements is an immutable set of capabilities required by an
// effective feature configuration.
type BackendRequirements struct {
	required map[BackendCapability]struct{}
}

// BackendFeature names an existing framework feature whose effective use can
// be translated into provider-neutral backend requirements.
type BackendFeature string

const (
	BackendFeatureCheckpointPersistence BackendFeature = "checkpoint_persistence"
	BackendFeatureCrossInstanceHITL     BackendFeature = "cross_instance_hitl"
	BackendFeatureCheckpointExpiry      BackendFeature = "checkpoint_expiry"
	BackendFeatureWorkflow              BackendFeature = "workflow"
	BackendFeatureSchedulerProducer     BackendFeature = "scheduler_producer"
	BackendFeatureScheduledWorker       BackendFeature = "scheduled_worker"
	BackendFeatureTaskStorage           BackendFeature = "task_storage"
	BackendFeatureTaskQueue             BackendFeature = "task_queue"
	BackendFeatureDistributedLock       BackendFeature = "distributed_lock"
)

var knownBackendFeatures = map[BackendFeature][]BackendCapability{
	BackendFeatureCheckpointPersistence: {BackendCheckpoints},
	BackendFeatureCrossInstanceHITL:     {BackendCheckpoints, BackendCommands},
	BackendFeatureCheckpointExpiry:      {BackendCheckpoints, BackendCheckpointExpiry},
	BackendFeatureWorkflow:              {BackendWorkflowState},
	BackendFeatureSchedulerProducer:     {BackendSchedules, BackendTaskDispatcher},
	BackendFeatureScheduledWorker:       {BackendTaskConsumer},
	BackendFeatureTaskStorage:           {BackendTasks},
	BackendFeatureTaskQueue:             {BackendTaskQueue},
	BackendFeatureDistributedLock:       {BackendLock},
}

func NewBackendRequirements(capabilities ...BackendCapability) (BackendRequirements, error) {
	required := make(map[BackendCapability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if _, ok := knownBackendCapabilities[capability]; !ok {
			return BackendRequirements{}, fmt.Errorf("orchestration: unknown backend capability %q", capability)
		}
		if _, duplicate := required[capability]; duplicate {
			return BackendRequirements{}, fmt.Errorf("orchestration: duplicate backend capability %q", capability)
		}
		required[capability] = struct{}{}
	}
	return BackendRequirements{required: required}, nil
}

// RequirementsForFeatures derives requirements from the effective
// orchestrator debug configuration plus explicitly enabled framework features.
// HITL and scheduler concerns are explicit because no shared runtime config
// aggregate exists for them in the current codebase.
func RequirementsForFeatures(config *OrchestratorConfig, features ...BackendFeature) (BackendRequirements, error) {
	required := make(map[BackendCapability]struct{})
	if config != nil {
		if config.ExecutionStore.Enabled {
			required[BackendExecutionDebug] = struct{}{}
		}
		if config.LLMDebug.Enabled {
			required[BackendLLMDebug] = struct{}{}
		}
		if config.Skills.Enabled && len(config.Skills.Bindings) > 0 {
			required[BackendSkills] = struct{}{}
		}
	}
	seen := make(map[BackendFeature]struct{}, len(features))
	for _, feature := range features {
		capabilities, ok := knownBackendFeatures[feature]
		if !ok {
			return BackendRequirements{}, fmt.Errorf("orchestration: unknown backend feature %q", feature)
		}
		if _, duplicate := seen[feature]; duplicate {
			return BackendRequirements{}, fmt.Errorf("orchestration: duplicate backend feature %q", feature)
		}
		seen[feature] = struct{}{}
		for _, capability := range capabilities {
			required[capability] = struct{}{}
		}
	}
	capabilities := make([]BackendCapability, 0, len(required))
	for capability := range required {
		capabilities = append(capabilities, capability)
	}
	return NewBackendRequirements(capabilities...)
}

// Capabilities returns a sorted defensive copy of the required capabilities.
func (r BackendRequirements) Capabilities() []BackendCapability {
	capabilities := make([]BackendCapability, 0, len(r.required))
	for capability := range r.required {
		capabilities = append(capabilities, capability)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	return capabilities
}

// OrchestrationBackends is a provider-neutral composition value. Its private
// layout may grow without exposing provider concepts or exported struct fields.
type OrchestrationBackends struct {
	execution        ExecutionStore
	llmDebug         LLMDebugStore
	checkpoints      CheckpointPersistence
	checkpointExpiry ExpiredCheckpointSource
	commands         CommandStore
	workflow         StateStore
	schedules        core.ScheduleStore
	tasks            core.TaskStore
	taskQueue        core.TaskQueue
	taskDispatcher   core.TaskDispatcher
	taskConsumer     core.TaskConsumer
	lock             core.DistributedLock
	skillRegistry    SkillRegistry
	skillRevisions   SkillRevisionReader
	skillAdmin       SkillAdministrationStore
	skillDeletions   SkillRevisionDeletionStore
	skillAudit       SkillAuditSink
	runnables        []core.Runnable
}

type OrchestrationBackendOption interface {
	applyBackend(*OrchestrationBackends) error
}

type orchestrationBackendOption func(*OrchestrationBackends) error

func (option orchestrationBackendOption) applyBackend(backends *OrchestrationBackends) error {
	return option(backends)
}

func NewOrchestrationBackends(options ...OrchestrationBackendOption) (*OrchestrationBackends, error) {
	backends := &OrchestrationBackends{}
	return backends.With(options...)
}

func (b *OrchestrationBackends) With(overrides ...OrchestrationBackendOption) (*OrchestrationBackends, error) {
	if b == nil {
		return nil, fmt.Errorf("orchestration: backend composition is nil")
	}
	clone := *b
	clone.runnables = append([]core.Runnable(nil), b.runnables...)
	for index, override := range overrides {
		if isNilBackendValue(override) {
			return nil, fmt.Errorf("orchestration: backend option %d is nil", index)
		}
		if err := override.applyBackend(&clone); err != nil {
			return nil, err
		}
	}
	return &clone, nil
}

func backendOption[T any](name string, value T, assign func(*OrchestrationBackends, T)) OrchestrationBackendOption {
	return orchestrationBackendOption(func(backends *OrchestrationBackends) error {
		if isNilBackendValue(value) {
			return fmt.Errorf("orchestration: %s backend is nil", name)
		}
		assign(backends, value)
		return nil
	})
}

func WithExecutionBackend(value ExecutionStore) OrchestrationBackendOption {
	return backendOption("execution", value, func(b *OrchestrationBackends, v ExecutionStore) { b.execution = v })
}
func WithLLMDebugBackend(value LLMDebugStore) OrchestrationBackendOption {
	return backendOption("llm debug", value, func(b *OrchestrationBackends, v LLMDebugStore) { b.llmDebug = v })
}
func WithCheckpointPersistence(value CheckpointPersistence) OrchestrationBackendOption {
	return backendOption("checkpoint persistence", value, func(b *OrchestrationBackends, v CheckpointPersistence) { b.checkpoints = v })
}
func WithCheckpointExpiry(value ExpiredCheckpointSource) OrchestrationBackendOption {
	return backendOption("checkpoint expiry", value, func(b *OrchestrationBackends, v ExpiredCheckpointSource) { b.checkpointExpiry = v })
}
func WithCommandBackend(value CommandStore) OrchestrationBackendOption {
	return backendOption("command", value, func(b *OrchestrationBackends, v CommandStore) { b.commands = v })
}
func WithWorkflowBackend(value StateStore) OrchestrationBackendOption {
	return backendOption("workflow", value, func(b *OrchestrationBackends, v StateStore) { b.workflow = v })
}
func WithScheduleBackend(value core.ScheduleStore) OrchestrationBackendOption {
	return backendOption("schedule", value, func(b *OrchestrationBackends, v core.ScheduleStore) { b.schedules = v })
}
func WithTaskBackend(value core.TaskStore) OrchestrationBackendOption {
	return backendOption("task", value, func(b *OrchestrationBackends, v core.TaskStore) { b.tasks = v })
}
func WithTaskQueueBackend(value core.TaskQueue) OrchestrationBackendOption {
	return backendOption("task queue", value, func(b *OrchestrationBackends, v core.TaskQueue) { b.taskQueue = v })
}
func WithTaskDispatcherBackend(value core.TaskDispatcher) OrchestrationBackendOption {
	return backendOption("task dispatcher", value, func(b *OrchestrationBackends, v core.TaskDispatcher) { b.taskDispatcher = v })
}
func WithTaskConsumerBackend(value core.TaskConsumer) OrchestrationBackendOption {
	return backendOption("task consumer", value, func(b *OrchestrationBackends, v core.TaskConsumer) { b.taskConsumer = v })
}
func WithLockBackend(value core.DistributedLock) OrchestrationBackendOption {
	return backendOption("lock", value, func(b *OrchestrationBackends, v core.DistributedLock) { b.lock = v })
}

// WithSkillRegistryBackend follows the established composition naming and
// leaves WithSkillRegistry available for the agent-facing orchestrator option.
func WithSkillRegistryBackend(value SkillRegistry) OrchestrationBackendOption {
	return backendOption("skill registry", value, func(b *OrchestrationBackends, v SkillRegistry) { b.skillRegistry = v })
}
func WithSkillRevisionReader(value SkillRevisionReader) OrchestrationBackendOption {
	return backendOption("skill revision reader", value, func(b *OrchestrationBackends, v SkillRevisionReader) { b.skillRevisions = v })
}
func WithSkillAdministrationStore(value SkillAdministrationStore) OrchestrationBackendOption {
	return backendOption("skill administration", value, func(b *OrchestrationBackends, v SkillAdministrationStore) { b.skillAdmin = v })
}
func WithSkillRevisionDeletionStore(value SkillRevisionDeletionStore) OrchestrationBackendOption {
	return backendOption("skill revision deletion", value, func(b *OrchestrationBackends, v SkillRevisionDeletionStore) { b.skillDeletions = v })
}
func WithSkillAuditSink(value SkillAuditSink) OrchestrationBackendOption {
	return backendOption("skill audit", value, func(b *OrchestrationBackends, v SkillAuditSink) { b.skillAudit = v })
}

func WithRunnables(values ...core.Runnable) OrchestrationBackendOption {
	snapshot := append([]core.Runnable(nil), values...)
	return orchestrationBackendOption(func(backends *OrchestrationBackends) error {
		for index, value := range snapshot {
			if isNilBackendValue(value) {
				return fmt.Errorf("orchestration: runnable %d is nil", index)
			}
		}
		backends.runnables = append([]core.Runnable(nil), snapshot...)
		return nil
	})
}

func (b *OrchestrationBackends) Execution() ExecutionStore {
	if b == nil {
		return nil
	}
	return b.execution
}
func (b *OrchestrationBackends) LLMDebug() LLMDebugStore {
	if b == nil {
		return nil
	}
	return b.llmDebug
}
func (b *OrchestrationBackends) Checkpoints() CheckpointPersistence {
	if b == nil {
		return nil
	}
	return b.checkpoints
}
func (b *OrchestrationBackends) CheckpointExpiry() ExpiredCheckpointSource {
	if b == nil {
		return nil
	}
	return b.checkpointExpiry
}
func (b *OrchestrationBackends) Commands() CommandStore {
	if b == nil {
		return nil
	}
	return b.commands
}
func (b *OrchestrationBackends) Workflow() StateStore {
	if b == nil {
		return nil
	}
	return b.workflow
}
func (b *OrchestrationBackends) Schedules() core.ScheduleStore {
	if b == nil {
		return nil
	}
	return b.schedules
}
func (b *OrchestrationBackends) Tasks() core.TaskStore {
	if b == nil {
		return nil
	}
	return b.tasks
}
func (b *OrchestrationBackends) TaskQueue() core.TaskQueue {
	if b == nil {
		return nil
	}
	return b.taskQueue
}
func (b *OrchestrationBackends) TaskDispatcher() core.TaskDispatcher {
	if b == nil {
		return nil
	}
	return b.taskDispatcher
}
func (b *OrchestrationBackends) TaskConsumer() core.TaskConsumer {
	if b == nil {
		return nil
	}
	return b.taskConsumer
}
func (b *OrchestrationBackends) Lock() core.DistributedLock {
	if b == nil {
		return nil
	}
	return b.lock
}
func (b *OrchestrationBackends) SkillRegistry() SkillRegistry {
	if b == nil {
		return nil
	}
	return b.skillRegistry
}
func (b *OrchestrationBackends) SkillRevisionReader() SkillRevisionReader {
	if b == nil {
		return nil
	}
	return b.skillRevisions
}
func (b *OrchestrationBackends) SkillAdministrationStore() SkillAdministrationStore {
	if b == nil {
		return nil
	}
	return b.skillAdmin
}
func (b *OrchestrationBackends) SkillRevisionDeletionStore() SkillRevisionDeletionStore {
	if b == nil {
		return nil
	}
	return b.skillDeletions
}
func (b *OrchestrationBackends) SkillAuditSink() SkillAuditSink {
	if b == nil {
		return nil
	}
	return b.skillAudit
}
func (b *OrchestrationBackends) Runnables() []core.Runnable {
	if b == nil {
		return nil
	}
	return append([]core.Runnable(nil), b.runnables...)
}

func (b *OrchestrationBackends) ValidateFor(requirements BackendRequirements) error {
	if b == nil {
		return fmt.Errorf("orchestration: backend composition is nil")
	}
	missing := make([]string, 0)
	for _, capability := range requirements.Capabilities() {
		if !b.has(capability) {
			missing = append(missing, string(capability))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("orchestration: missing required backend capabilities: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (b *OrchestrationBackends) has(capability BackendCapability) bool {
	switch capability {
	case BackendExecutionDebug:
		return !isNilBackendValue(b.execution)
	case BackendLLMDebug:
		return !isNilBackendValue(b.llmDebug)
	case BackendCheckpoints:
		return !isNilBackendValue(b.checkpoints)
	case BackendCheckpointExpiry:
		return !isNilBackendValue(b.checkpointExpiry)
	case BackendCommands:
		return !isNilBackendValue(b.commands)
	case BackendWorkflowState:
		return !isNilBackendValue(b.workflow)
	case BackendSchedules:
		return !isNilBackendValue(b.schedules)
	case BackendTasks:
		return !isNilBackendValue(b.tasks)
	case BackendTaskQueue:
		return !isNilBackendValue(b.taskQueue)
	case BackendTaskDispatcher:
		return !isNilBackendValue(b.taskDispatcher)
	case BackendTaskConsumer:
		return !isNilBackendValue(b.taskConsumer)
	case BackendLock:
		return !isNilBackendValue(b.lock)
	case BackendSkills:
		return !isNilBackendValue(b.skillRegistry)
	default:
		return false
	}
}

func isNilBackendValue(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
