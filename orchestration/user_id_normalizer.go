package orchestration

import "strings"

// UserIDNormalizer canonicalizes a user_id before it is used as the isolation
// key for user memory. It is applied at the two framework chokepoints where
// user_id enters the user-memory subsystem — recall (enrichment) and storage
// (extraction) — so a single normalizer guarantees the same person reads and
// writes the same memory bucket regardless of how the caller cased the id.
//
// The framework default is identity (no-op): user_id stays case-sensitive and
// verbatim, preserving existing behavior for callers that deliberately treat
// distinct casings as distinct users. Opt in to canonicalization with an
// explicit normalizer via WithUserIDNormalizer (Layer 1 builder) or the
// per-hook options (Layer 3).
//
// Normalization changes the identity key, so it is a deliberate code-level
// policy — made once, consistently — not a per-deployment toggle: flipping it
// against a populated store would silently fork or orphan user-memory buckets.
// That is why it is a WithXXX() behavioural plug and not an environment
// variable (per FRAMEWORK_DESIGN_PRINCIPLES Configuration Split).
type UserIDNormalizer func(string) string

// IdentityUserIDNormalizer returns the user_id unchanged. This is the framework
// default — case-sensitive, verbatim user isolation.
func IdentityUserIDNormalizer(id string) string { return id }

// NormalizeUserIDLowercaseTrim canonicalizes a user_id by trimming surrounding
// whitespace and lowercasing. This is the recommended canonical form: it folds
// "Joe", " joe ", and "JOE" into a single bucket, removing the case-split class
// of cross-session memory surprises.
func NormalizeUserIDLowercaseTrim(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
