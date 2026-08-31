package redisprovider

import (
	"fmt"
	"os"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

// DefaultBackendsOption customizes the Layer-1 Redis composition path. The
// underlying client, provider, and backend options remain available so callers
// can replace one concern without abandoning the convenience constructor.
type DefaultBackendsOption interface {
	applyDefaultBackends(*defaultBackendsConfig) error
}

type defaultBackendsOptionFunc func(*defaultBackendsConfig) error

func (option defaultBackendsOptionFunc) applyDefaultBackends(config *defaultBackendsConfig) error {
	return option(config)
}

type defaultBackendsConfig struct {
	roles           []ClientRole
	clientOptions   []ClientConfigOption
	providerOptions []Option
	overrides       []orchestration.OrchestrationBackendOption
}

var clientRoleCapabilities = map[ClientRole][]orchestration.BackendCapability{
	ClientRoleExecution: {orchestration.BackendExecutionDebug},
	ClientRoleLLMDebug:  {orchestration.BackendLLMDebug},
	ClientRoleHITL: {
		orchestration.BackendCheckpoints,
		orchestration.BackendCheckpointExpiry,
		orchestration.BackendCommands,
	},
	ClientRoleWorkflow: {orchestration.BackendWorkflowState},
	ClientRoleScheduling: {
		orchestration.BackendSchedules,
		orchestration.BackendTasks,
		orchestration.BackendTaskQueue,
		orchestration.BackendTaskDispatcher,
		orchestration.BackendTaskConsumer,
		orchestration.BackendLock,
	},
	ClientRoleSkills: {
		orchestration.BackendSkillRegistry,
		orchestration.BackendSkillRevisionReader,
		orchestration.BackendSkillAdministration,
		orchestration.BackendSkillRevisionDeletion,
		orchestration.BackendSkillAudit,
	},
}

var providerOptionVariableRoles = map[string]ClientRole{
	"TRUVAG3_LLM_DEBUG_TTL":             ClientRoleLLMDebug,
	"TRUVAG3_LLM_DEBUG_ERROR_TTL":       ClientRoleLLMDebug,
	"TRUVAG3_HITL_CHECKPOINT_TTL":       ClientRoleHITL,
	"TRUVAG3_WORKFLOW_STATE_TTL":        ClientRoleWorkflow,
	"TRUVAG3_TASK_QUEUE_RETRY_ATTEMPTS": ClientRoleScheduling,
	"TRUVAG3_TASK_QUEUE_RETRY_DELAY":    ClientRoleScheduling,
}

// WithDefaultBackendRoles limits the convenience composition to the listed
// provider roles. With no role option, NewDefaultBackends constructs the full
// compatibility preset.
func WithDefaultBackendRoles(roles ...ClientRole) DefaultBackendsOption {
	snapshot := append([]ClientRole(nil), roles...)
	return defaultBackendsOptionFunc(func(config *defaultBackendsConfig) error {
		// Reuse the owned-client validator so the Layer-1 and Layer-2 paths have
		// identical unknown, empty, and duplicate-role behavior.
		validator := ownedClientsConfig{}
		if err := WithOwnedClientRoles(snapshot...).applyOwnedClients(&validator); err != nil {
			return err
		}
		config.roles = snapshot
		return nil
	})
}

// WithDefaultBackendClientConfig applies explicit client configuration after
// environment configuration. These options therefore retain the framework's
// code-over-environment precedence.
func WithDefaultBackendClientConfig(options ...ClientConfigOption) DefaultBackendsOption {
	snapshot := append([]ClientConfigOption(nil), options...)
	return defaultBackendsOptionFunc(func(config *defaultBackendsConfig) error {
		config.clientOptions = append(config.clientOptions, snapshot...)
		return nil
	})
}

// WithDefaultBackendProviderOptions applies explicit Redis preset options
// after environment configuration. It is the Layer-1 escape hatch for values
// such as namespace and provider-owned retention.
func WithDefaultBackendProviderOptions(options ...Option) DefaultBackendsOption {
	snapshot := append([]Option(nil), options...)
	return defaultBackendsOptionFunc(func(config *defaultBackendsConfig) error {
		config.providerOptions = append(config.providerOptions, snapshot...)
		return nil
	})
}

// WithDefaultBackendOverrides replaces individual provider-neutral backend
// capabilities after the Redis preset has been assembled.
func WithDefaultBackendOverrides(overrides ...orchestration.OrchestrationBackendOption) DefaultBackendsOption {
	snapshot := append([]orchestration.OrchestrationBackendOption(nil), overrides...)
	return defaultBackendsOptionFunc(func(config *defaultBackendsConfig) error {
		config.overrides = append(config.overrides, snapshot...)
		return nil
	})
}

// OwnedBackends holds a Layer-1 backend composition together with the Redis
// clients created for it. Close releases only those provider-owned clients.
type OwnedBackends struct {
	backends *orchestration.OrchestrationBackends
	clients  *OwnedClients
}

// Backends returns the provider-neutral composition value. Runtime components
// should still receive only the narrow contracts they consume.
func (owned *OwnedBackends) Backends() *orchestration.OrchestrationBackends {
	if owned == nil {
		return nil
	}
	return owned.backends
}

// Close releases every Redis client created by NewDefaultBackends. It is safe
// to call more than once.
func (owned *OwnedBackends) Close() error {
	if owned == nil || owned.clients == nil {
		return nil
	}
	return owned.clients.Close()
}

// NewDefaultBackends is the Layer-1 Redis composition path. It resolves the
// documented environment configuration, applies explicit code options last,
// creates only the requested client roles, and returns an ownership handle for
// deterministic cleanup. The implementation delegates to the same Layer-2 and
// Layer-3 constructors available for direct use.
func NewDefaultBackends(logger core.Logger, options ...DefaultBackendsOption) (*OwnedBackends, error) {
	return newDefaultBackends(logger, os.LookupEnv, options...)
}

func newDefaultBackends(
	logger core.Logger,
	lookup func(string) (string, bool),
	options ...DefaultBackendsOption,
) (*OwnedBackends, error) {
	if lookup == nil {
		return nil, fmt.Errorf("redisprovider: environment lookup is required")
	}
	configured := defaultBackendsConfig{}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("redisprovider: default-backends option %d is nil", index)
		}
		if err := option.applyDefaultBackends(&configured); err != nil {
			return nil, err
		}
	}
	lookup = lookupForDefaultBackendRoles(lookup, configured.roles)

	clientConfig, err := LoadClientConfigFromEnvironment(DefaultClientConfig(), lookup)
	if err != nil {
		return nil, err
	}
	clientConfig, err = ConfigureClientConfig(clientConfig, configured.clientOptions...)
	if err != nil {
		return nil, err
	}
	ownedClientOptions := []OwnedClientsOption(nil)
	if len(configured.roles) > 0 {
		ownedClientOptions = append(ownedClientOptions, WithOwnedClientRoles(configured.roles...))
	}
	clients, err := NewOwnedClients(clientConfig, ownedClientOptions...)
	if err != nil {
		return nil, err
	}

	providerOptions, err := NewOptions()
	if err != nil {
		_ = clients.Close()
		return nil, err
	}
	providerOptions, err = LoadOptionsFromEnvironment(providerOptions, lookup)
	if err != nil {
		_ = clients.Close()
		return nil, err
	}
	codeOptions := make([]Option, 0, len(configured.providerOptions)+1)
	codeOptions = append(codeOptions, WithLogger(logger))
	codeOptions = append(codeOptions, configured.providerOptions...)
	providerOptions, err = ConfigureOptions(providerOptions, codeOptions...)
	if err != nil {
		_ = clients.Close()
		return nil, err
	}

	backends, err := NewOrchestrationBackends(clients.ClientSet(), providerOptions, configured.overrides...)
	if err != nil {
		_ = clients.Close()
		return nil, err
	}
	roles := configured.roles
	if len(roles) == 0 {
		roles = orderedClientRoles
	}
	required := make(map[orchestration.BackendCapability]struct{})
	for _, role := range roles {
		for _, capability := range clientRoleCapabilities[role] {
			required[capability] = struct{}{}
		}
	}
	if providerOptions.expiryEnabled {
		required[orchestration.BackendCheckpointExpiryProcessor] = struct{}{}
	}
	capabilities := make([]orchestration.BackendCapability, 0, len(required))
	for capability := range required {
		capabilities = append(capabilities, capability)
	}
	requirements, err := orchestration.NewBackendRequirements(capabilities...)
	if err != nil {
		_ = clients.Close()
		return nil, err
	}
	if err := backends.ValidateFor(requirements); err != nil {
		_ = clients.Close()
		return nil, err
	}
	return &OwnedBackends{backends: backends, clients: clients}, nil
}

func lookupForDefaultBackendRoles(
	lookup func(string) (string, bool),
	roles []ClientRole,
) func(string) (string, bool) {
	if len(roles) == 0 {
		return lookup
	}
	selected := make(map[ClientRole]struct{}, len(roles))
	for _, role := range roles {
		selected[role] = struct{}{}
	}
	variableRoles := make(map[string]ClientRole, len(clientRoleDatabaseVariables)+len(providerOptionVariableRoles))
	for role, name := range clientRoleDatabaseVariables {
		variableRoles[name] = role
	}
	for name, role := range providerOptionVariableRoles {
		variableRoles[name] = role
	}
	return func(name string) (string, bool) {
		if role, roleSpecific := variableRoles[name]; roleSpecific {
			if _, enabled := selected[role]; !enabled {
				return "", false
			}
		}
		return lookup(name)
	}
}
