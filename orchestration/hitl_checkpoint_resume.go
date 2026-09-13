package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const checkpointTransactionAttempts = 8

// TIME at commit prevents a lease expiring between the watched read and EXEC
// from authorizing a stale write. Payload JSON is never decoded/re-encoded by
// Lua. The record and both indexes share the existing agent hash tag.
const writeCheckpointTransactionScript = `
local deadline = tonumber(ARGV[5])
if deadline > 0 then
    local now = redis.call("TIME")
    if tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000) >= deadline then
        return -3
    end
end
if ARGV[4] == "keep" then
    if redis.call("EXISTS", KEYS[1]) == 0 then return -2 end
    redis.call("SET", KEYS[1], ARGV[1], "KEEPTTL")
else
    redis.call("SET", KEYS[1], ARGV[1])
    local ttl = tonumber(ARGV[4])
    if ttl > 0 then redis.call("PEXPIRE", KEYS[1], ttl) end
end
local ttl = redis.call("PTTL", KEYS[1])
local function retain_index(key)
    local existed = redis.call("EXISTS", key)
    redis.call("SADD", key, ARGV[2])
    if ttl > 0 then
        local previous = redis.call("PTTL", key)
        if existed == 0 or (previous >= 0 and previous < ttl) then
            redis.call("PEXPIRE", key, ttl)
        end
    elseif ttl == -1 then
        redis.call("PERSIST", key)
    end
end
if ARGV[3] == "pending" then retain_index(KEYS[2])
else redis.call("SREM", KEYS[2], ARGV[2]) end
if ARGV[6] == "1" then retain_index(KEYS[3]) end
return 1
`

var _ CheckpointResumePersistence = (*RedisCheckpointStore)(nil)

func isCheckpointDecisionStatus(status CheckpointStatus) bool {
	switch status {
	case CheckpointStatusApproved, CheckpointStatusRejected, CheckpointStatusAborted,
		CheckpointStatusExpired, CheckpointStatusExpiredApproved,
		CheckpointStatusExpiredRejected, CheckpointStatusExpiredAborted:
		return true
	default:
		return false
	}
}

func validateResumeIdentity(name, value string) error {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 256 {
		return fmt.Errorf("orchestration: %s must contain 1-256 bytes without surrounding whitespace", name)
	}
	return nil
}

func validateResumeLease(lease time.Duration) error {
	if lease < 3*time.Second || lease > 24*time.Hour {
		return errors.New("orchestration: resume claim lease must be between 3s and 24h")
	}
	return nil
}

// WATCH pins the transaction and its TIME command to the checkpoint's Redis
// slot. Only optimistic contention is retried; backend and state errors retain
// their original identities. All checkpoint keys belong to the same agent tag.
func (s *RedisCheckpointStore) checkpointTransaction(ctx context.Context, ids []string, fn func(*redis.Tx) error) error {
	keys := make([]string, len(ids))
	for i, id := range ids {
		if err := validateResumeIdentity("checkpoint ID", id); err != nil {
			return err
		}
		keys[i] = s.keys.checkpoint(id)
	}
	for attempt := 0; attempt < checkpointTransactionAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.client.Watch(ctx, fn, keys...)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	return &ErrCheckpointCASExhausted{CheckpointID: ids[0]}
}

func (s *RedisCheckpointStore) readWatchedCheckpoint(ctx context.Context, tx *redis.Tx, id string) (*ExecutionCheckpoint, error) {
	data, err := tx.Get(ctx, s.keys.checkpoint(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, &ErrCheckpointNotFound{CheckpointID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("read checkpoint transaction: %w", err)
	}
	var checkpoint ExecutionCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("decode checkpoint transaction: %w", err)
	}
	if checkpoint.CheckpointID != id {
		return nil, &ErrCheckpointSuccessorConflict{CheckpointID: id}
	}
	return &checkpoint, nil
}

func (s *RedisCheckpointStore) writeWatchedCheckpoint(ctx context.Context, tx *redis.Tx, checkpoint *ExecutionCheckpoint, create bool, leaseDeadline time.Time) error {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode checkpoint transaction: %w", err)
	}
	retention := interface{}("keep")
	if create {
		retention = s.ttl.Milliseconds()
	}
	deadline := int64(0)
	if !leaseDeadline.IsZero() {
		deadline = leaseDeadline.UnixMilli()
	}
	requestIndex := "0"
	if checkpoint.RequestID != "" {
		requestIndex = "1"
	}
	var command *redis.Cmd
	_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		command = pipe.Eval(ctx, writeCheckpointTransactionScript,
			[]string{s.keys.checkpoint(checkpoint.CheckpointID), s.keys.pending(), s.keys.request(checkpoint.RequestID)},
			data, checkpoint.CheckpointID, string(checkpoint.Status), retention, deadline, requestIndex)
		return nil
	})
	if err != nil {
		return err
	}
	status, err := command.Int64()
	if err != nil {
		return err
	}
	switch status {
	case 1:
		return nil
	case -3:
		return &ErrCheckpointResumeClaimLost{CheckpointID: checkpoint.CheckpointID}
	case -2:
		return &ErrCheckpointNotFound{CheckpointID: checkpoint.CheckpointID}
	}
	return fmt.Errorf("unexpected checkpoint transaction result: %d", status)
}

// ClaimCheckpointForResume atomically acquires an approval or recovers an
// expired attempt. Recovery keeps the original reserved successor identity.
func (s *RedisCheckpointStore) ClaimCheckpointForResume(ctx context.Context, request ResumeClaimRequest) (*CheckpointResumeClaim, error) {
	for name, value := range map[string]string{"attempt ID": request.AttemptID, "owner": request.Owner, "successor checkpoint ID": request.SuccessorCheckpointID} {
		if err := validateResumeIdentity(name, value); err != nil {
			return nil, err
		}
	}
	if len(request.Owner) > maxCheckpointClaimOwnerLen || request.SuccessorCheckpointID == request.CheckpointID {
		return nil, errors.New("orchestration: invalid resume owner or successor identity")
	}
	if err := validateResumeLease(request.Lease); err != nil {
		return nil, err
	}
	var claim *CheckpointResumeClaim
	err := s.checkpointTransaction(ctx, []string{request.CheckpointID}, func(tx *redis.Tx) error {
		checkpoint, err := s.readWatchedCheckpoint(ctx, tx, request.CheckpointID)
		if err != nil {
			return err
		}
		now, err := tx.Time(ctx).Result()
		if err != nil {
			return fmt.Errorf("read resume backend time: %w", err)
		}
		previous, successor, successorAttempt := checkpoint.Status, request.SuccessorCheckpointID, request.AttemptID
		if checkpoint.Status == CheckpointStatusResuming {
			state := checkpoint.ResumeState
			if state == nil || !IsResumableStatus(state.PreviousStatus) || state.ReservedSuccessorCheckpointID == "" {
				return &ErrCheckpointSuccessorConflict{CheckpointID: request.CheckpointID}
			}
			if state.LeaseExpiresAt.After(now) {
				return &ErrCheckpointResumeInProgress{CheckpointID: request.CheckpointID}
			}
			if state.AttemptID == request.AttemptID {
				return &ErrCheckpointResumeClaimLost{CheckpointID: request.CheckpointID}
			}
			previous, successor = state.PreviousStatus, state.ReservedSuccessorCheckpointID
			if err := tx.Watch(ctx, s.keys.checkpoint(successor)).Err(); err != nil {
				return err
			}
			child, childErr := s.readWatchedCheckpoint(ctx, tx, successor)
			if childErr != nil && !IsCheckpointNotFound(childErr) {
				return childErr
			}
			if child != nil {
				if child.ParentCheckpointID != request.CheckpointID || child.ParentResumeAttemptID != state.ReservedSuccessorAttemptID {
					return &ErrCheckpointSuccessorConflict{CheckpointID: request.CheckpointID}
				}
				successorAttempt = state.ReservedSuccessorAttemptID
			}
		} else if !IsResumableStatus(checkpoint.Status) {
			return &ErrCheckpointNotResumable{CheckpointID: request.CheckpointID, Status: checkpoint.Status}
		}
		checkpoint.Status = CheckpointStatusResuming
		checkpoint.ResumeState = &CheckpointResumeState{
			AttemptID: request.AttemptID, Owner: request.Owner, PreviousStatus: previous,
			LeaseExpiresAt: now.Add(request.Lease), ReservedSuccessorCheckpointID: successor,
			ReservedSuccessorAttemptID: successorAttempt,
		}
		if err := s.writeWatchedCheckpoint(ctx, tx, checkpoint, false, checkpoint.ResumeState.LeaseExpiresAt); err != nil {
			return err
		}
		checkpoint.Status = previous
		claim = &CheckpointResumeClaim{Checkpoint: checkpoint, AttemptID: request.AttemptID, PreviousStatus: previous, LeaseExpiresAt: checkpoint.ResumeState.LeaseExpiresAt}
		return nil
	})
	return claim, err
}

func (s *RedisCheckpointStore) readOwnedCheckpoint(ctx context.Context, tx *redis.Tx, checkpointID, attemptID string) (*ExecutionCheckpoint, error) {
	checkpoint, err := s.readWatchedCheckpoint(ctx, tx, checkpointID)
	if IsCheckpointNotFound(err) {
		return nil, &ErrCheckpointResumeClaimLost{CheckpointID: checkpointID}
	}
	if err != nil {
		return nil, err
	}
	now, err := tx.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("read resume backend time: %w", err)
	}
	if checkpoint.Status != CheckpointStatusResuming || checkpoint.ResumeState == nil || checkpoint.ResumeState.AttemptID != attemptID || !checkpoint.ResumeState.LeaseExpiresAt.After(now) {
		return nil, &ErrCheckpointResumeClaimLost{CheckpointID: checkpointID}
	}
	return checkpoint, nil
}

// RenewCheckpointResumeClaim extends only a still-live owned lease.
func (s *RedisCheckpointStore) RenewCheckpointResumeClaim(ctx context.Context, checkpointID, attemptID string, lease time.Duration) error {
	if err := validateResumeLease(lease); err != nil {
		return err
	}
	return s.checkpointTransaction(ctx, []string{checkpointID}, func(tx *redis.Tx) error {
		checkpoint, err := s.readOwnedCheckpoint(ctx, tx, checkpointID, attemptID)
		if err != nil {
			return err
		}
		now, err := tx.Time(ctx).Result()
		if err != nil {
			return fmt.Errorf("read renewal backend time: %w", err)
		}
		deadline := checkpoint.ResumeState.LeaseExpiresAt
		checkpoint.ResumeState.LeaseExpiresAt = now.Add(lease)
		return s.writeWatchedCheckpoint(ctx, tx, checkpoint, false, deadline)
	})
}

// ReleaseCheckpointResumeClaim restores approval only when no successor exists.
func (s *RedisCheckpointStore) ReleaseCheckpointResumeClaim(ctx context.Context, checkpointID, attemptID string) error {
	return s.checkpointTransaction(ctx, []string{checkpointID}, func(tx *redis.Tx) error {
		checkpoint, err := s.readOwnedCheckpoint(ctx, tx, checkpointID, attemptID)
		if err != nil {
			return err
		}
		successor := checkpoint.ResumeState.ReservedSuccessorCheckpointID
		if err := tx.Watch(ctx, s.keys.checkpoint(successor)).Err(); err != nil {
			return err
		}
		_, err = s.readWatchedCheckpoint(ctx, tx, successor)
		if err == nil {
			return &ErrCheckpointSuccessorConflict{CheckpointID: checkpointID}
		}
		if !IsCheckpointNotFound(err) {
			return err
		}
		deadline := checkpoint.ResumeState.LeaseExpiresAt
		checkpoint.Status = checkpoint.ResumeState.PreviousStatus
		checkpoint.ResumeState = nil
		return s.writeWatchedCheckpoint(ctx, tx, checkpoint, false, deadline)
	})
}

// FinalizeCheckpointResume consumes an owned approval. Continued finalization
// verifies the authoritative child inside the same watched transaction.
func (s *RedisCheckpointStore) FinalizeCheckpointResume(ctx context.Context, finalization ResumeFinalization) error {
	if finalization.Outcome != ResumeFinalizationCompleted && finalization.Outcome != ResumeFinalizationContinued {
		return errors.New("orchestration: invalid resume finalization outcome")
	}
	return s.checkpointTransaction(ctx, []string{finalization.CheckpointID}, func(tx *redis.Tx) error {
		checkpoint, err := s.readOwnedCheckpoint(ctx, tx, finalization.CheckpointID, finalization.AttemptID)
		if err != nil {
			return err
		}
		successorID := checkpoint.ResumeState.ReservedSuccessorCheckpointID
		if err := tx.Watch(ctx, s.keys.checkpoint(successorID)).Err(); err != nil {
			return err
		}
		successor, successorErr := s.readWatchedCheckpoint(ctx, tx, successorID)
		if successorErr != nil && !IsCheckpointNotFound(successorErr) {
			return successorErr
		}
		if finalization.Outcome == ResumeFinalizationContinued {
			if successorErr != nil || finalization.SuccessorCheckpointID != successorID || successor.ParentCheckpointID != checkpoint.CheckpointID || successor.ParentResumeAttemptID != checkpoint.ResumeState.ReservedSuccessorAttemptID || successor.Status == CheckpointStatusPreparing {
				return &ErrCheckpointSuccessorConflict{CheckpointID: checkpoint.CheckpointID}
			}
			checkpoint.Status = CheckpointStatusContinued
			checkpoint.ResumeState.SuccessorCheckpointID = successorID
		} else {
			if successorErr == nil || finalization.SuccessorCheckpointID != "" {
				return &ErrCheckpointSuccessorConflict{CheckpointID: checkpoint.CheckpointID}
			}
			checkpoint.Status = CheckpointStatusCompleted
		}
		checkpoint.ResumeState.Outcome = finalization.Outcome
		return s.writeWatchedCheckpoint(ctx, tx, checkpoint, false, checkpoint.ResumeState.LeaseExpiresAt)
	})
}
