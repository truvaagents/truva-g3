package redisprovider

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

// Removed Redis routing and prefix settings are rejected only for selected roles.
// Empty values are absent, consistent with the topology configuration resolver.
var removedRedisEnvironmentVariables = map[string]ClientRole{
	"TRUVAG3_EXECUTION_DEBUG_REDIS_DB":   ClientRoleExecution,
	"TRUVAG3_LLM_DEBUG_REDIS_DB":         ClientRoleLLMDebug,
	"TRUVAG3_HITL_REDIS_DB":              ClientRoleHITL,
	"TRUVAG3_WORKFLOW_REDIS_DB":          ClientRoleWorkflow,
	"TRUVAG3_SCHEDULING_REDIS_DB":        ClientRoleScheduling,
	"TRUVAG3_SKILLS_REDIS_DB":            ClientRoleSkills,
	"TRUVAG3_EXECUTION_DEBUG_KEY_PREFIX": ClientRoleExecution,
	"TRUVAG3_LLM_DEBUG_KEY_PREFIX":       ClientRoleLLMDebug,
	"TRUVAG3_HITL_KEY_PREFIX":            ClientRoleHITL,
}

type ClientConfig struct {
	connection core.RedisConnectionConfig
}

type ClientConfigOption interface{ applyClientConfig(*ClientConfig) error }
type clientConfigOption func(*ClientConfig) error

type completeConnectionConfigOption struct {
	apply clientConfigOption
}

func (option clientConfigOption) applyClientConfig(config *ClientConfig) error { return option(config) }

func (option completeConnectionConfigOption) applyClientConfig(config *ClientConfig) error {
	return option.apply(config)
}

func (completeConnectionConfigOption) replacesRedisConnectionSource() {}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		connection: core.DefaultRedisConnectionConfig(),
	}
}

func ConfigureClientConfig(base ClientConfig, options ...ClientConfigOption) (ClientConfig, error) {
	configured := cloneClientConfig(base)
	for index, option := range options {
		if option == nil {
			return ClientConfig{}, fmt.Errorf("redisprovider: client-config option %d is nil", index)
		}
		if err := option.applyClientConfig(&configured); err != nil {
			return ClientConfig{}, err
		}
	}
	resolution, err := core.ResolveRedisConnectionConfig(&configured.connection, nil)
	if err != nil {
		return ClientConfig{}, fmt.Errorf("redisprovider: Redis connection: %w", err)
	}
	configured.connection = resolution
	return configured, nil
}

func WithClientURL(url string) ClientConfigOption {
	return completeConnectionConfigOption{apply: func(config *ClientConfig) error {
		url = strings.TrimSpace(url)
		if url == "" {
			return fmt.Errorf("redisprovider: Redis URL is required")
		}
		connection, err := core.ParseStandaloneRedisURL(url)
		if err != nil {
			return fmt.Errorf("redisprovider: Redis URL: %w", err)
		}
		config.connection = connection
		return nil
	}}
}

// WithConnectionConfig replaces the complete Redis topology profile. It is the
// direct Go configuration path and is validated after all options are applied.
func WithConnectionConfig(connection core.RedisConnectionConfig) ClientConfigOption {
	return completeConnectionConfigOption{apply: func(config *ClientConfig) error {
		config.connection = connection
		return nil
	}}
}

// WithConnectionMode selects standalone, Sentinel, or cluster topology.
func WithConnectionMode(mode core.RedisMode) ClientConfigOption {
	return clientConfigOption(func(config *ClientConfig) error {
		config.connection.Mode = mode
		return nil
	})
}

// WithAddresses replaces the Redis seed or Sentinel address list.
func WithAddresses(addresses ...string) ClientConfigOption {
	snapshot := append([]string(nil), addresses...)
	return clientConfigOption(func(config *ClientConfig) error {
		config.connection.Addrs = append([]string(nil), snapshot...)
		return nil
	})
}

// WithSentinelMasterName sets the explicit Sentinel master name.
func WithSentinelMasterName(masterName string) ClientConfigOption {
	return clientConfigOption(func(config *ClientConfig) error {
		config.connection.MasterName = masterName
		return nil
	})
}

// WithTLSConfig sets transport security without transferring ownership of the
// caller's mutable TLS configuration.
func WithTLSConfig(tlsConfig *tls.Config) ClientConfigOption {
	return clientConfigOption(func(config *ClientConfig) error {
		if tlsConfig == nil {
			config.connection.TLSConfig = nil
		} else {
			config.connection.TLSConfig = tlsConfig.Clone()
		}
		return nil
	})
}

// WithDatabase sets the shared logical database. Canonical provider
// composition is portable across standalone, Sentinel, and cluster topology,
// so only DB 0 is accepted.
func WithDatabase(database int) ClientConfigOption {
	return clientConfigOption(func(config *ClientConfig) error {
		if database != 0 {
			return fmt.Errorf("redisprovider: canonical Redis connections require DB 0: %w", core.ErrInvalidConfiguration)
		}
		config.connection.DB = database
		return nil
	})
}

func LoadClientConfigFromEnvironment(base ClientConfig, lookup func(string) (string, bool)) (ClientConfig, error) {
	return loadClientConfigFromEnvironment(base, lookup, true)
}

func loadClientConfigFromEnvironment(
	base ClientConfig,
	lookup func(string) (string, bool),
	loadConnection bool,
) (ClientConfig, error) {
	if lookup == nil {
		return ClientConfig{}, fmt.Errorf("redisprovider: environment lookup is required")
	}
	configured := cloneClientConfig(base)
	if loadConnection {
		resolution, err := core.ResolveRedisConnectionConfig(nil, core.LookupEnv(lookup))
		if err != nil {
			return ClientConfig{}, fmt.Errorf("redisprovider: resolve Redis connection: %w", err)
		}
		configured.connection = resolution
	}
	for name := range removedRedisEnvironmentVariables {
		if value, present := lookup(name); present && strings.TrimSpace(value) != "" {
			return ClientConfig{}, fmt.Errorf("redisprovider: %s is unsupported; use DB 0 with RedisKeyspace: %w", name, core.ErrInvalidConfiguration)
		}
	}
	return ConfigureClientConfig(configured)
}

func hasCompleteRedisConnectionOption(options []ClientConfigOption) bool {
	for _, option := range options {
		if _, complete := option.(interface{ replacesRedisConnectionSource() }); complete {
			return true
		}
	}
	return false
}

func cloneClientConfig(config ClientConfig) ClientConfig {
	clone := ClientConfig{connection: config.connection}
	clone.connection.Addrs = append([]string(nil), config.connection.Addrs...)
	if config.connection.TLSConfig != nil {
		clone.connection.TLSConfig = config.connection.TLSConfig.Clone()
	}
	return clone
}

type OwnedClients struct {
	clientSet *ClientSet
	clients   []redis.UniversalClient
}

type ownedClientsConfig struct {
	restricted bool
	roles      map[ClientRole]struct{}
}

// OwnedClientsOption controls which configured Redis roles receive owned
// clients. Every selected role shares the same DB-0 connection.
type OwnedClientsOption interface {
	applyOwnedClients(*ownedClientsConfig) error
}

type ownedClientsOptionFunc func(*ownedClientsConfig) error

func (option ownedClientsOptionFunc) applyOwnedClients(config *ownedClientsConfig) error {
	return option(config)
}

// WithOwnedClientRoles limits owned-client construction to the listed roles.
// This is the feature-aware path for processes such as an agent that needs only
// the runtime skill registry.
func WithOwnedClientRoles(roles ...ClientRole) OwnedClientsOption {
	snapshot := append([]ClientRole(nil), roles...)
	return ownedClientsOptionFunc(func(config *ownedClientsConfig) error {
		if len(snapshot) == 0 {
			return fmt.Errorf("redisprovider: at least one owned-client role is required")
		}
		selected := make(map[ClientRole]struct{}, len(snapshot))
		for _, role := range snapshot {
			if _, ok := knownClientRoles[role]; !ok {
				return fmt.Errorf("redisprovider: unknown client role %q", role)
			}
			if _, duplicate := selected[role]; duplicate {
				return fmt.Errorf("redisprovider: duplicate owned-client role %q", role)
			}
			selected[role] = struct{}{}
		}
		config.restricted = true
		config.roles = selected
		return nil
	})
}

func NewOwnedClients(config ClientConfig, options ...OwnedClientsOption) (*OwnedClients, error) {
	configured, err := ConfigureClientConfig(config)
	if err != nil {
		return nil, err
	}
	ownedConfig := ownedClientsConfig{}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("redisprovider: owned-client option %d is nil", index)
		}
		if err := option.applyOwnedClients(&ownedConfig); err != nil {
			return nil, err
		}
	}

	client, err := core.NewRedisUniversalClient(configured.connection)
	if err != nil {
		return nil, fmt.Errorf("redisprovider: create shared client: %w", err)
	}
	var clientSet *ClientSet
	if ownedConfig.restricted {
		roleOptions := make([]ClientSetOption, 0, len(ownedConfig.roles))
		for _, role := range orderedClientRoles {
			if _, selected := ownedConfig.roles[role]; selected {
				roleOptions = append(roleOptions, WithRoleClient(role, client))
			}
		}
		clientSet, err = NewClientSet(nil, roleOptions...)
	} else {
		clientSet, err = NewClientSet(client)
	}
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &OwnedClients{clientSet: clientSet, clients: []redis.UniversalClient{client}}, nil
}

func (clients *OwnedClients) ClientSet() *ClientSet {
	if clients == nil {
		return nil
	}
	return clients.clientSet
}

func (clients *OwnedClients) Close() error {
	if clients == nil {
		return nil
	}
	var firstErr error
	for _, client := range clients.clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	clients.clients = nil
	return firstErr
}
