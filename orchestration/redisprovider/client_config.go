package redisprovider

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

type ClientConfig struct {
	url    string
	roleDB map[ClientRole]int
}

type ClientConfigOption interface{ applyClientConfig(*ClientConfig) error }
type clientConfigOption func(*ClientConfig) error

func (option clientConfigOption) applyClientConfig(config *ClientConfig) error { return option(config) }

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		url: "redis://localhost:6379",
		roleDB: map[ClientRole]int{
			ClientRoleExecution:  core.RedisDBExecutionDebug,
			ClientRoleLLMDebug:   core.RedisDBLLMDebug,
			ClientRoleHITL:       core.RedisDBTelemetry,
			ClientRoleWorkflow:   core.RedisDBServiceDiscovery,
			ClientRoleScheduling: core.RedisDBServiceDiscovery,
		},
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
	if _, err := redis.ParseURL(configured.url); err != nil {
		return ClientConfig{}, fmt.Errorf("redisprovider: invalid Redis URL: %w", core.ErrInvalidConfiguration)
	}
	return configured, nil
}

func WithClientURL(url string) ClientConfigOption {
	return clientConfigOption(func(config *ClientConfig) error {
		url = strings.TrimSpace(url)
		if url == "" {
			return fmt.Errorf("redisprovider: Redis URL is required")
		}
		config.url = url
		return nil
	})
}

func WithRoleDatabase(role ClientRole, database int) ClientConfigOption {
	return clientConfigOption(func(config *ClientConfig) error {
		if _, ok := knownClientRoles[role]; !ok {
			return fmt.Errorf("redisprovider: unknown client role %q", role)
		}
		if database < 0 {
			return fmt.Errorf("redisprovider: database for role %q cannot be negative", role)
		}
		if config.roleDB == nil {
			config.roleDB = make(map[ClientRole]int)
		}
		config.roleDB[role] = database
		return nil
	})
}

func LoadClientConfigFromEnvironment(base ClientConfig, lookup func(string) (string, bool)) (ClientConfig, error) {
	if lookup == nil {
		return ClientConfig{}, fmt.Errorf("redisprovider: environment lookup is required")
	}
	options := make([]ClientConfigOption, 0, 6)
	if value, ok := lookup("REDIS_URL"); ok {
		options = append(options, WithClientURL(value))
	} else if value, ok := lookup("TRUVAG3_REDIS_URL"); ok {
		options = append(options, WithClientURL(value))
	}
	for role, name := range map[ClientRole]string{
		ClientRoleExecution:  "TRUVAG3_EXECUTION_DEBUG_REDIS_DB",
		ClientRoleLLMDebug:   "TRUVAG3_LLM_DEBUG_REDIS_DB",
		ClientRoleHITL:       "TRUVAG3_HITL_REDIS_DB",
		ClientRoleWorkflow:   "TRUVAG3_WORKFLOW_REDIS_DB",
		ClientRoleScheduling: "TRUVAG3_SCHEDULING_REDIS_DB",
	} {
		if value, ok := lookup(name); ok {
			database, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return ClientConfig{}, fmt.Errorf("redisprovider: %s must be an integer", name)
			}
			options = append(options, WithRoleDatabase(role, database))
		}
	}
	return ConfigureClientConfig(base, options...)
}

func cloneClientConfig(config ClientConfig) ClientConfig {
	clone := ClientConfig{url: config.url, roleDB: make(map[ClientRole]int, len(config.roleDB))}
	for role, database := range config.roleDB {
		clone.roleDB[role] = database
	}
	return clone
}

type OwnedClients struct {
	clientSet *ClientSet
	clients   []redis.UniversalClient
}

func NewOwnedClients(config ClientConfig) (*OwnedClients, error) {
	configured, err := ConfigureClientConfig(config)
	if err != nil {
		return nil, err
	}
	byDatabase := make(map[int]redis.UniversalClient)
	roleOptions := make([]ClientSetOption, 0, len(configured.roleDB))
	for _, role := range []ClientRole{ClientRoleExecution, ClientRoleLLMDebug, ClientRoleHITL, ClientRoleWorkflow, ClientRoleScheduling} {
		database, ok := configured.roleDB[role]
		if !ok {
			continue
		}
		client := byDatabase[database]
		if client == nil {
			redisOptions, parseErr := redis.ParseURL(configured.url)
			if parseErr != nil {
				return nil, fmt.Errorf("redisprovider: invalid Redis URL: %w", core.ErrInvalidConfiguration)
			}
			redisOptions.DB = database
			client = redis.NewClient(redisOptions)
			byDatabase[database] = client
		}
		roleOptions = append(roleOptions, WithRoleClient(role, client))
	}
	clientSet, err := NewClientSet(nil, roleOptions...)
	if err != nil {
		for _, client := range byDatabase {
			_ = client.Close()
		}
		return nil, err
	}
	owned := &OwnedClients{clientSet: clientSet, clients: make([]redis.UniversalClient, 0, len(byDatabase))}
	for _, client := range byDatabase {
		owned.clients = append(owned.clients, client)
	}
	return owned, nil
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
