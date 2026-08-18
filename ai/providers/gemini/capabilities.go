package gemini

import "strings"

const (
	capabilitySnapshotVersion       = "gemini-generate-content-v1beta-2026-08-17"
	lifecycleExclusionThresholdDays = 45
	geminiInputTokenLimit           = 1_048_576
	geminiOutputTokenLimit          = 65_536
	geminiLifecycleSource           = "https://ai.google.dev/gemini-api/docs/deprecations"
	geminiModelCapabilitySource     = "https://ai.google.dev/gemini-api/docs/models"
	geminiThinkingCapabilitySource  = "https://ai.google.dev/gemini-api/docs/generate-content/thinking"
	geminiLatestModelContractSource = "https://ai.google.dev/gemini-api/docs/latest-model"
)

type thinkingLevelSet uint8

const (
	thinkingMinimal thinkingLevelSet = 1 << iota
	thinkingLow
	thinkingMedium
	thinkingHigh
)

type generationMethodSet uint8

const (
	methodGenerate generationMethodSet = 1 << iota
	methodStream
)

type wireSurfaceSet uint8

const surfaceGenerateContent wireSurfaceSet = 1 << iota

type modelLifecycle uint8

const (
	lifecycleStable modelLifecycle = iota
	lifecyclePreview
)

type floatCapability struct {
	Supported  bool
	HasDefault bool
	Default    float64
	Min        float64
	Max        float64
}

type intCapability struct {
	Supported  bool
	HasDefault bool
	Default    int
	Min        int
}

type modelCapabilities struct {
	ModelID                  string
	LifecycleStage           modelLifecycle
	Methods                  generationMethodSet
	Surfaces                 wireSurfaceSet
	InputTokenLimit          int
	OutputTokenLimit         int
	ThinkingLevels           thinkingLevelSet
	Temperature              floatCapability
	TopP                     floatCapability
	TopK                     intCapability
	ForbidTemperature        bool
	ForbidTopP               bool
	ForbidTopK               bool
	ForbidCandidateCount     bool
	RejectPrefilledModelTurn bool
}

type modelCoverageDecision struct {
	ModelID              string
	Surface              string
	LifecycleSource      string
	CapabilitySource     string
	EvaluatedOn          string
	ShutdownOn           string
	DaysRemaining        int
	Replacement          string
	Included             bool
	ExclusionExplanation string
}

func lifecycleAllowsInclusion(shutdownOn string, daysRemaining int) bool {
	return strings.TrimSpace(shutdownOn) == "" || daysRemaining >= lifecycleExclusionThresholdDays
}

// This is dated evidence, not runtime discovery. The Gemini 2.5 models remain
// covered because the authoritative public Gemini API schedule had no
// announced shutdown date on 2026-08-17.
var modelCoverageSnapshot = [...]modelCoverageDecision{
	{ModelID: "gemini-2.5-pro", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-2.5-flash", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-2.5-flash-lite", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-3.1-pro-preview", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-3.1-flash-lite", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", ShutdownOn: "2027-05-07", DaysRemaining: 263, Replacement: "gemini-3.5-flash-lite", Included: true},
	{ModelID: "gemini-3-flash-preview", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Replacement: "gemini-3.6-flash", Included: true},
	{ModelID: "gemini-3.5-flash", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-3.5-flash-lite", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-3.6-flash", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-3.7-flash", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", DaysRemaining: -1, Included: true},
	{ModelID: "gemini-3-pro-preview", Surface: "generate-content", LifecycleSource: geminiLifecycleSource, CapabilitySource: geminiModelCapabilitySource, EvaluatedOn: "2026-08-17", ShutdownOn: "2026-03-09", DaysRemaining: -161, Replacement: "gemini-3.1-pro-preview", Included: false, ExclusionExplanation: "retired before the capability snapshot"},
}

var defaultSamplingCapabilities = struct {
	temperature floatCapability
	topP        floatCapability
	topK        intCapability
}{
	temperature: floatCapability{Supported: true, HasDefault: true, Default: 1, Min: 0, Max: 2},
	topP:        floatCapability{Supported: true, HasDefault: true, Default: 0.95, Min: 0, Max: 1},
	topK:        intCapability{Supported: true, HasDefault: true, Default: 64, Min: 1},
}

func supportedCapabilities(model string, lifecycle modelLifecycle, levels thinkingLevelSet) modelCapabilities {
	return modelCapabilities{
		ModelID:          model,
		LifecycleStage:   lifecycle,
		Methods:          methodGenerate | methodStream,
		Surfaces:         surfaceGenerateContent,
		InputTokenLimit:  geminiInputTokenLimit,
		OutputTokenLimit: geminiOutputTokenLimit,
		ThinkingLevels:   levels,
		Temperature:      defaultSamplingCapabilities.temperature,
		TopP:             defaultSamplingCapabilities.topP,
		TopK:             defaultSamplingCapabilities.topK,
	}
}

func latestCapabilities(model string, lifecycle modelLifecycle, levels thinkingLevelSet) modelCapabilities {
	capabilities := supportedCapabilities(model, lifecycle, levels)
	capabilities.Temperature = floatCapability{}
	capabilities.TopP = floatCapability{}
	capabilities.TopK = intCapability{}
	capabilities.ForbidTemperature = true
	capabilities.ForbidTopP = true
	capabilities.ForbidTopK = true
	capabilities.ForbidCandidateCount = true
	capabilities.RejectPrefilledModelTurn = true
	return capabilities
}

func withCandidateCountForbidden(capabilities modelCapabilities) modelCapabilities {
	capabilities.ForbidCandidateCount = true
	return capabilities
}

// Sources frozen on 2026-08-17: model IDs and token limits come from the
// model guide/model pages, reasoning subsets from the GenerateContent thinking
// guide, and sampling/candidate/turn prohibitions from the latest-model guide.
// Keep the otherwise-unused URL constants above searchable so a refresh starts
// from the exact same-surface evidence rather than from a cross-surface page.
var capabilitySnapshot = [...]modelCapabilities{
	supportedCapabilities("gemini-2.5-pro", lifecycleStable, 0),
	supportedCapabilities("gemini-2.5-flash", lifecycleStable, 0),
	supportedCapabilities("gemini-2.5-flash-lite", lifecycleStable, 0),
	withCandidateCountForbidden(supportedCapabilities("gemini-3.1-pro-preview", lifecyclePreview, thinkingLow|thinkingMedium|thinkingHigh)),
	withCandidateCountForbidden(supportedCapabilities("gemini-3.1-flash-lite", lifecycleStable, thinkingMinimal|thinkingLow|thinkingMedium|thinkingHigh)),
	withCandidateCountForbidden(supportedCapabilities("gemini-3-flash-preview", lifecyclePreview, thinkingMinimal|thinkingLow|thinkingMedium|thinkingHigh)),
	withCandidateCountForbidden(supportedCapabilities("gemini-3.5-flash", lifecycleStable, thinkingMinimal|thinkingLow|thinkingMedium|thinkingHigh)),
	latestCapabilities("gemini-3.5-flash-lite", lifecycleStable, thinkingMinimal|thinkingLow|thinkingMedium|thinkingHigh),
	latestCapabilities("gemini-3.6-flash", lifecycleStable, thinkingMinimal|thinkingLow|thinkingMedium|thinkingHigh),
	latestCapabilities("gemini-3.7-flash", lifecycleStable, thinkingLow|thinkingMedium|thinkingHigh),
}

func capabilitiesForModel(model string) (modelCapabilities, bool) {
	for _, capabilities := range capabilitySnapshot {
		if capabilities.ModelID == model {
			return capabilities, true
		}
	}
	return modelCapabilities{}, false
}

func conservativeUnknownCapabilities(model string) modelCapabilities {
	return modelCapabilities{
		ModelID:                  model,
		Methods:                  methodGenerate | methodStream,
		Surfaces:                 surfaceGenerateContent,
		ForbidTemperature:        true,
		ForbidTopP:               true,
		ForbidTopK:               true,
		ForbidCandidateCount:     true,
		RejectPrefilledModelTurn: true,
	}
}

func (capabilities modelCapabilities) forbidsSampling() bool {
	return capabilities.ForbidTemperature || capabilities.ForbidTopP || capabilities.ForbidTopK
}

func (levels thinkingLevelSet) supports(level string) bool {
	var required thinkingLevelSet
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "minimal":
		required = thinkingMinimal
	case "low":
		required = thinkingLow
	case "medium":
		required = thinkingMedium
	case "high":
		required = thinkingHigh
	default:
		return false
	}
	return levels&required != 0
}
