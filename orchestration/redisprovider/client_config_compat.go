package redisprovider

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

var clientRoleDatabaseVariables = map[ClientRole]string{
	ClientRoleExecution:  "TRUVAG3_EXECUTION_DEBUG_REDIS_DB",
	ClientRoleLLMDebug:   "TRUVAG3_LLM_DEBUG_REDIS_DB",
	ClientRoleHITL:       "TRUVAG3_HITL_REDIS_DB",
	ClientRoleWorkflow:   "TRUVAG3_WORKFLOW_REDIS_DB",
	ClientRoleScheduling: "TRUVAG3_SCHEDULING_REDIS_DB",
	ClientRoleSkills:     "TRUVAG3_SKILLS_REDIS_DB",
}

// WithRoleDatabase retains numbered standalone routing for the precursor
// compatibility window.
// Deprecated: use one DB-0 connection with versioned RedisKeyspace prefixes.
func WithRoleDatabase(role ClientRole, database int) ClientConfigOption {
	return clientConfigOption(func(config *ClientConfig) error {
		if _, ok := knownClientRoles[role]; !ok {
			return fmt.Errorf("redisprovider: unknown client role %q", role)
		}
		if database < 0 || database > 15 {
			return fmt.Errorf("redisprovider: database for role %q must be between 0 and 15", role)
		}
		if config.legacyRoleDB == nil {
			config.legacyRoleDB = make(map[ClientRole]int)
		}
		config.legacyRoleDB[role] = database
		config.diagnostics = appendRedisDiagnostic(
			config.diagnostics,
			"role-specific Redis databases are deprecated; use DB 0 with versioned key namespaces",
		)
		return nil
	})
}

func loadLegacyRoleDatabaseOptions(lookup func(string) (string, bool)) ([]ClientConfigOption, error) {
	options := make([]ClientConfigOption, 0, len(clientRoleDatabaseVariables))
	for role, name := range clientRoleDatabaseVariables {
		if value, ok := lookup(name); ok {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			database, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("redisprovider: %s must be an integer", name)
			}
			options = append(options, WithRoleDatabase(role, database))
		}
	}
	return options, nil
}

func newLegacyRoleDatabaseClients(configured ClientConfig, ownedConfig ownedClientsConfig) (*OwnedClients, error) {
	if configured.connection.Mode != core.RedisModeStandalone {
		return nil, fmt.Errorf("redisprovider: role-specific databases require deprecated standalone mode: %w", core.ErrInvalidConfiguration)
	}
	byDatabase := make(map[int]redis.UniversalClient)
	roleOptions := make([]ClientSetOption, 0, len(orderedClientRoles))
	for _, role := range orderedClientRoles {
		if ownedConfig.restricted {
			if _, selected := ownedConfig.roles[role]; !selected {
				continue
			}
		}
		database := configured.connection.DB
		if override, present := configured.legacyRoleDB[role]; present {
			database = override
		}
		client := byDatabase[database]
		if client == nil {
			profile := configured.connection
			profile.DB = database
			var err error
			client, err = core.NewRedisUniversalClientForCompatibility(profile)
			if err != nil {
				for _, ownedClient := range byDatabase {
					_ = ownedClient.Close()
				}
				return nil, fmt.Errorf("redisprovider: create deprecated %s DB client: %w", role, err)
			}
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
	clients := make([]redis.UniversalClient, 0, len(byDatabase))
	for _, client := range byDatabase {
		clients = append(clients, client)
	}
	return &OwnedClients{clientSet: clientSet, clients: clients}, nil
}
