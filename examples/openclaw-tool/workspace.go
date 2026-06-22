package main

import (
	"crypto/rand"
	"encoding/hex"
)

// newSessionID returns a fresh, unique value sent as the OpenResponses `user` field on every
// call, so each transaction is an independent conversation with no carryover (ANALYSIS.md §8/§13).
// Combined with the gateway's memory plugin set to "none", this is the statelessness story —
// we deliberately do NOT reset the on-disk workspace (that corrupts OpenClaw's live agent state;
// per-task file isolation is a §13 follow-up).
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "sess-0000000000000000"
	}
	return "sess-" + hex.EncodeToString(b)
}
