package orchestration

import "time"

// Shared defaults keep canonical configuration and direct constructors aligned.
// Explicit application configuration remains authoritative.
const (
	defaultPlanMaxTokens          = 20000
	defaultSynthesisMaxTokens     = 30000
	defaultStepTimeout            = 15 * time.Minute
	defaultOutboundHTTPTimeout    = 20 * time.Minute
	defaultMaxPhases              = 20
	defaultPhaseTimeout           = 25 * time.Minute
	defaultResultMaxBytes         = 64 * 1024
	defaultResultTotalBytes       = 192 * 1024
	defaultContinuationTotalBytes = 192 * 1024
	defaultSynthesisSkillTokens   = 6000
	defaultEventSummaryMaxTokens  = 10000
)
