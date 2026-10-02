package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

var (
	_ core.InvestigationLeaseCoordinator = (*AtomicLockCoordinator)(nil)
	_ core.InvestigationLeaseCoordinator = (*InMemoryInvestigationCoordinator)(nil)
)

func validateInvestigationLease(agent, entity, acquisition string, ttl time.Duration) error {
	if agent == "" || entity == "" || acquisition == "" {
		return errors.New("investigation lease requires agent, entity and acquisition identity")
	}
	if ttl < 0 {
		return errors.New("investigation lease TTL must be non-negative")
	}
	return nil
}

// One key is authoritative for both legacy strings and token-owned hashes.
// ARGV: agent, acquisition, TTL milliseconds, entity (index member).
var acquireInvestigationLeaseScript = redis.NewScript(`
local kind = redis.call('TYPE', KEYS[1]).ok
if kind == 'none' then
  redis.call('HSET', KEYS[1], 'agent', ARGV[1], 'acquisition', ARGV[2])
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  redis.call('SADD', KEYS[2], ARGV[4])
  return {1, '', tonumber(ARGV[3])}
end
if kind == 'string' then return {0, redis.call('GET', KEYS[1]), 0} end
if kind ~= 'hash' then return redis.error_reply('invalid investigation state') end
local agent = redis.call('HGET', KEYS[1], 'agent')
local acquisition = redis.call('HGET', KEYS[1], 'acquisition')
local ttl = redis.call('PTTL', KEYS[1])
if not agent or not acquisition or ttl <= 0 then return redis.error_reply('invalid investigation lease') end
if agent == ARGV[1] and acquisition == ARGV[2] then return {1, '', ttl} end
return {0, agent, 0}
`)

var releaseInvestigationLeaseScript = redis.NewScript(`
local kind = redis.call('TYPE', KEYS[1]).ok
if kind == 'none' then
  redis.call('SREM', KEYS[2], ARGV[3])
  return 'absent'
end
if kind ~= 'hash' then return 'not_owned' end
if redis.call('HGET', KEYS[1], 'agent') ~= ARGV[1] or redis.call('HGET', KEYS[1], 'acquisition') ~= ARGV[2] then
  return 'not_owned'
end
redis.call('DEL', KEYS[1])
redis.call('SREM', KEYS[2], ARGV[3])
return 'released'
`)

// Reads only human-readable ownership; private acquisition IDs never leave via
// the legacy holder/list API. Malformed backend state remains a visible failure.
var investigationHolderScript = redis.NewScript(`
local kind = redis.call('TYPE', KEYS[1]).ok
if kind == 'none' then return false end
if kind == 'string' then return redis.call('GET', KEYS[1]) end
if kind == 'hash' then
  local agent = redis.call('HGET', KEYS[1], 'agent')
  if agent and redis.call('HGET', KEYS[1], 'acquisition') then return agent end
end
return redis.error_reply('invalid investigation state')
`)

func (v *AtomicLockCoordinator) AcquireInvestigation(ctx context.Context, request core.InvestigationLeaseRequest) (*core.InvestigationLease, string, error) {
	if err := validateInvestigationLease(request.AgentName, request.EntityID, request.AcquisitionID, request.TTL); err != nil {
		return nil, "", err
	}
	ttl := request.TTL
	if ttl == 0 {
		ttl = v.defaultTTL
	}
	milliseconds := max(ttl.Milliseconds(), int64(1))
	result, err := acquireInvestigationLeaseScript.Run(ctx, v.client,
		[]string{v.investigationKey(request.EntityID), v.investigationIndexKey()},
		request.AgentName, request.AcquisitionID, milliseconds, request.EntityID).Slice()
	if err != nil {
		return nil, "", fmt.Errorf("acquire investigation lease: %w", err)
	}
	if len(result) != 3 {
		return nil, "", errors.New("invalid investigation acquisition response")
	}
	claimed, ok := result[0].(int64)
	holder, holderOK := result[1].(string)
	remaining, ttlOK := result[2].(int64)
	if !ok || !holderOK || !ttlOK || (claimed != 0 && claimed != 1) || (claimed == 1 && remaining <= 0) {
		return nil, "", errors.New("invalid investigation acquisition response")
	}
	if claimed == 0 {
		return nil, holder, nil
	}
	return &core.InvestigationLease{AgentName: request.AgentName, EntityID: request.EntityID,
		AcquisitionID: request.AcquisitionID, ExpiresAt: time.Now().Add(time.Duration(remaining) * time.Millisecond)}, "", nil
}

func (v *AtomicLockCoordinator) ReleaseInvestigationLease(ctx context.Context, lease core.InvestigationLease) (core.InvestigationReleaseOutcome, error) {
	if err := validateInvestigationLease(lease.AgentName, lease.EntityID, lease.AcquisitionID, 0); err != nil {
		return core.InvestigationUnknown, err
	}
	value, err := releaseInvestigationLeaseScript.Run(ctx, v.client,
		[]string{v.investigationKey(lease.EntityID), v.investigationIndexKey()},
		lease.AgentName, lease.AcquisitionID, lease.EntityID).Text()
	if err != nil {
		return core.InvestigationUnknown, fmt.Errorf("release investigation lease: %w", err)
	}
	outcome := core.InvestigationReleaseOutcome(value)
	switch outcome {
	case core.InvestigationReleased, core.InvestigationAbsent, core.InvestigationNotOwned:
		return outcome, nil
	default:
		return core.InvestigationUnknown, errors.New("invalid investigation release response")
	}
}

func (m *InMemoryInvestigationCoordinator) AcquireInvestigation(ctx context.Context, request core.InvestigationLeaseRequest) (*core.InvestigationLease, string, error) {
	if err := validateInvestigationLease(request.AgentName, request.EntityID, request.AcquisitionID, request.TTL); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	ttl := request.TTL
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	ttl = time.Duration(max(ttl.Milliseconds(), int64(1))) * time.Millisecond
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	claim, exists := m.claims[request.EntityID]
	if exists && now.Before(claim.expiresAt) {
		if claim.agentName != request.AgentName || claim.acquisitionID != request.AcquisitionID {
			return nil, claim.agentName, nil
		}
	} else {
		claim = investigationClaim{agentName: request.AgentName, acquisitionID: request.AcquisitionID, expiresAt: now.Add(ttl)}
		m.claims[request.EntityID] = claim
	}
	return &core.InvestigationLease{AgentName: request.AgentName, EntityID: request.EntityID,
		AcquisitionID: request.AcquisitionID, ExpiresAt: claim.expiresAt}, "", nil
}

func (m *InMemoryInvestigationCoordinator) ReleaseInvestigationLease(ctx context.Context, lease core.InvestigationLease) (core.InvestigationReleaseOutcome, error) {
	if err := validateInvestigationLease(lease.AgentName, lease.EntityID, lease.AcquisitionID, 0); err != nil {
		return core.InvestigationUnknown, err
	}
	if err := ctx.Err(); err != nil {
		return core.InvestigationUnknown, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	claim, exists := m.claims[lease.EntityID]
	if !exists || !time.Now().Before(claim.expiresAt) {
		delete(m.claims, lease.EntityID)
		return core.InvestigationAbsent, nil
	}
	if claim.agentName != lease.AgentName || claim.acquisitionID != lease.AcquisitionID {
		return core.InvestigationNotOwned, nil
	}
	delete(m.claims, lease.EntityID)
	return core.InvestigationReleased, nil
}
