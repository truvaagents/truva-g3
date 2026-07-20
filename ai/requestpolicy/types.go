// Package requestpolicy applies provider-scoped request rules to isolated
// logical drafts. It deliberately depends only on Core and the standard
// library so providers can share one mutation engine without importing ai.
package requestpolicy

import (
	"context"
	"fmt"

	"github.com/truvaagents/truva-g3/core"
)

// RequestInfo is the immutable identity used to select request policies.
type RequestInfo struct {
	Provider       string
	ProviderAlias  string
	Surface        string
	Operation      string
	Purpose        string
	RequestedModel string
	ResolvedModel  string
}

// RequestEditor is the constrained mutation surface exposed to middleware.
type RequestEditor interface {
	Info() RequestInfo
	Get(path string) (interface{}, bool)
	Set(path string, value interface{}) error
	Remove(path string) error
	SetHeader(name, value string) error
	RemoveHeader(name string) error
}

// Draft adapts a provider-local logical request to the policy engine.
type Draft interface {
	RequestEditor
	Validate() error
}

// RequestMiddleware performs a named, versioned logical request edit.
// Implementations may be invoked concurrently and must not retain the editor.
type RequestMiddleware interface {
	Name() string
	Version() string
	Apply(context.Context, RequestEditor) error
}

// StableRequestMiddleware is implemented by middleware whose name and version
// fully identify deterministic request-policy semantics. Middleware is treated
// as fingerprint-unstable unless it explicitly implements this interface and
// returns true.
type StableRequestMiddleware interface {
	RequestMiddleware
	StablePolicyFingerprint() bool
}

// CompatibilityMode controls how built-in compatibility rules interact with
// explicit presence-aware request intent.
type CompatibilityMode uint8

const (
	// CompatibilityCompatible allows built-in rules to adjust explicit intent
	// and reports the adjustment.
	CompatibilityCompatible CompatibilityMode = iota
	// CompatibilityStrict rejects an unacknowledged built-in adjustment to an
	// explicitly supplied new-API value.
	CompatibilityStrict
)

// Config is defensively snapshotted by NewEngine.
type Config struct {
	BuiltIns   []core.AIProviderPatch
	AppRules   []core.AIProviderPatch
	Middleware []RequestMiddleware
	Mode       CompatibilityMode
}

// PolicyError is a structured pre-network policy failure.
type PolicyError struct {
	Stage string
	Rule  string
	Path  string
	Err   error
}

func (e *PolicyError) Error() string {
	if e == nil {
		return "request policy failed"
	}
	message := "request policy " + e.Stage + " failed"
	if e.Rule != "" {
		message += fmt.Sprintf(" for %q", e.Rule)
	}
	if e.Path != "" {
		message += fmt.Sprintf(" at %q", e.Path)
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

// Unwrap exposes the underlying validation or middleware error.
func (e *PolicyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// explicitIntentDraft is implemented by provider drafts that can distinguish
// presence-aware new-API intent from inherited legacy values.
type explicitIntentDraft interface {
	HasExplicitIntent(path string) bool
}

// fingerprintDraft supplies the versioned provider-surface identity included
// in stable policy fingerprints.
type fingerprintDraft interface {
	PolicyFingerprintIdentity() string
}

type headerReader interface {
	Header(name string) (string, bool)
}

type setChangeDetector interface {
	WouldSetChange(path string, value interface{}) bool
}
