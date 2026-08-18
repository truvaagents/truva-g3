// Package redisprovider composes the included Redis adapters behind
// provider-neutral orchestration contracts. Runtime orchestration does not
// import this package.
package redisprovider

import (
	"fmt"
	"reflect"

	"github.com/redis/go-redis/v9"
)

type ClientRole string

const (
	ClientRoleExecution  ClientRole = "execution"
	ClientRoleLLMDebug   ClientRole = "llm_debug"
	ClientRoleHITL       ClientRole = "hitl"
	ClientRoleWorkflow   ClientRole = "workflow"
	ClientRoleScheduling ClientRole = "scheduling"
	ClientRoleSkills     ClientRole = "skills"
)

var knownClientRoles = map[ClientRole]struct{}{
	ClientRoleExecution: {}, ClientRoleLLMDebug: {}, ClientRoleHITL: {},
	ClientRoleWorkflow: {}, ClientRoleScheduling: {},
	ClientRoleSkills: {},
}

type ClientSet struct {
	defaultClient redis.UniversalClient
	roleClients   map[ClientRole]redis.UniversalClient
}

type ClientSetOption interface{ applyClientSet(*ClientSet) error }
type clientSetOption func(*ClientSet) error

func (option clientSetOption) applyClientSet(clients *ClientSet) error { return option(clients) }

func WithRoleClient(role ClientRole, client redis.UniversalClient) ClientSetOption {
	return clientSetOption(func(clients *ClientSet) error {
		if _, ok := knownClientRoles[role]; !ok {
			return fmt.Errorf("redisprovider: unknown client role %q", role)
		}
		if nilRedisClient(client) {
			return fmt.Errorf("redisprovider: client for role %q is nil", role)
		}
		clients.roleClients[role] = client
		return nil
	})
}

func NewClientSet(defaultClient redis.UniversalClient, options ...ClientSetOption) (*ClientSet, error) {
	clients := &ClientSet{defaultClient: defaultClient, roleClients: make(map[ClientRole]redis.UniversalClient)}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("redisprovider: client-set option %d is nil", index)
		}
		if err := option.applyClientSet(clients); err != nil {
			return nil, err
		}
	}
	if nilRedisClient(clients.defaultClient) && len(clients.roleClients) == 0 {
		return nil, fmt.Errorf("redisprovider: at least one Redis client is required")
	}
	return clients, nil
}

func (clients *ClientSet) Resolve(role ClientRole) redis.UniversalClient {
	if clients == nil {
		return nil
	}
	if _, ok := knownClientRoles[role]; !ok {
		return nil
	}
	if client := clients.roleClients[role]; !nilRedisClient(client) {
		return client
	}
	return clients.defaultClient
}

func nilRedisClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	return value.Kind() == reflect.Pointer && value.IsNil()
}
