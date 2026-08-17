package orchestration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultSkillRuntimeConfiguration(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	wantLimits := SkillRuntimeLimits{
		MaxBindings: 32, MaxAutoCandidates: 16, CatalogTokenBudget: 2000,
		MaxResourceCandidates: 32, ResourceCatalogTokenBudget: 2000,
		MaxActiveSkills: 6, TotalTokenBudget: 8192, MainTokenBudget: 6144,
		ResourceTokenBudget: 4096, MaxResourcesPerPhase: 2,
		MaxResourcesPerExecution: 8, ResolutionMaxTokens: 512,
		RegistryReadTimeout: 5 * time.Second, SynthesisTokenBudget: 2048,
		EffectiveInputTokenBudget: 0,
	}
	if config.Skills.Enabled || len(config.Skills.Bindings) != 0 ||
		config.Skills.DomainCompatibilityMode != SkillDomainCompatibilityWarn ||
		config.Skills.Cache != (SkillContentCacheConfig{Mode: SkillContentCacheLocal, MaxBytes: 16 * 1024 * 1024}) ||
		config.Skills.Limits != wantLimits {
		t.Fatalf("default skills config = %#v", config.Skills)
	}
	if err := ValidateOrchestratorConfig(config); err != nil {
		t.Fatalf("ValidateOrchestratorConfig(default) error = %v", err)
	}
}

func TestResolveOrchestratorConfigSkillEnvironmentAndDefaults(t *testing.T) {
	registry := &cacheTestSkillRegistry{}
	result, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_SKILLS_ENABLED":                      "true",
			"TRUVAG3_SKILL_BINDINGS_JSON":                 `[{"namespace":"travel","name":"weather","activation":"auto"}]`,
			"TRUVAG3_SKILL_ACTIVATION_MODEL":              "fast-activation",
			"TRUVAG3_SKILL_RESOURCE_MODEL":                "fast-resource",
			"TRUVAG3_SKILL_DOMAIN_COMPATIBILITY_MODE":     "enforce",
			"TRUVAG3_SKILL_CACHE_MODE":                    "local",
			"TRUVAG3_SKILL_CACHE_MAX_BYTES":               "2097152",
			"TRUVAG3_SKILL_MAX_BINDINGS":                  "20",
			"TRUVAG3_SKILL_MAX_AUTO_CANDIDATES":           "10",
			"TRUVAG3_SKILL_CATALOG_TOKEN_BUDGET":          "1500",
			"TRUVAG3_SKILL_MAX_RESOURCE_CANDIDATES":       "20",
			"TRUVAG3_SKILL_RESOURCE_CATALOG_TOKEN_BUDGET": "1500",
			"TRUVAG3_SKILL_MAX_ACTIVE_SKILLS":             "5",
			"TRUVAG3_SKILL_TOTAL_TOKEN_BUDGET":            "6000",
			"TRUVAG3_SKILL_MAIN_TOKEN_BUDGET":             "4000",
			"TRUVAG3_SKILL_RESOURCE_TOKEN_BUDGET":         "3000",
			"TRUVAG3_SKILL_MAX_RESOURCES_PER_PHASE":       "2",
			"TRUVAG3_SKILL_MAX_RESOURCES_PER_EXECUTION":   "6",
			"TRUVAG3_SKILL_RESOLUTION_MAX_TOKENS":         "256",
			"TRUVAG3_SKILL_REGISTRY_READ_TIMEOUT":         "3s",
			"TRUVAG3_SKILL_SYNTHESIS_TOKEN_BUDGET":        "1500",
			"TRUVAG3_SKILL_EFFECTIVE_INPUT_TOKEN_BUDGET":  "32000",
		}),
		Options: []OrchestratorOption{WithSkillRegistry(registry)},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	config := result.Config
	if !config.Skills.Enabled || len(config.Skills.Bindings) != 1 ||
		config.Skills.Bindings[0].Version != "published" ||
		config.Skills.DomainCompatibilityMode != SkillDomainCompatibilityEnforce ||
		config.Skills.Cache != (SkillContentCacheConfig{Mode: SkillContentCacheLocal, MaxBytes: 2097152}) {
		t.Fatalf("resolved skills = %#v", config.Skills)
	}
	wantLimits := SkillRuntimeLimits{
		MaxBindings: 20, MaxAutoCandidates: 10, CatalogTokenBudget: 1500,
		MaxResourceCandidates: 20, ResourceCatalogTokenBudget: 1500,
		MaxActiveSkills: 5, TotalTokenBudget: 6000, MainTokenBudget: 4000,
		ResourceTokenBudget: 3000, MaxResourcesPerPhase: 2,
		MaxResourcesPerExecution: 6, ResolutionMaxTokens: 256,
		RegistryReadTimeout: 3 * time.Second, SynthesisTokenBudget: 1500,
		EffectiveInputTokenBudget: 32000,
	}
	if config.Skills.Limits != wantLimits {
		t.Fatalf("skill limits = %#v, want %#v", config.Skills.Limits, wantLimits)
	}
	if config.SkillActivationAIOptions == nil || config.SkillActivationAIOptions.Model == nil ||
		*config.SkillActivationAIOptions.Model != "fast-activation" ||
		config.SkillResourceAIOptions == nil || config.SkillResourceAIOptions.Model == nil ||
		*config.SkillResourceAIOptions.Model != "fast-resource" {
		t.Fatalf("skill model options = %#v / %#v", config.SkillActivationAIOptions, config.SkillResourceAIOptions)
	}
}

func TestSkillEnableAndBindingsEnvironmentAreCompleteAuthoritativeReplacement(t *testing.T) {
	registry := &cacheTestSkillRegistry{}
	result, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_SKILLS_ENABLED":      "true",
			"TRUVAG3_SKILL_BINDINGS_JSON": `[{"namespace":"env","name":"only","activation":"always"}]`,
		}),
		Options: []OrchestratorOption{
			WithSkills(SkillConfig{
				Enabled:  false,
				Bindings: []SkillBinding{{Namespace: "code", Name: "ignored", Activation: SkillActivationAuto}},
			}),
			WithSkillRegistry(registry),
		},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if !result.Config.Skills.Enabled || len(result.Config.Skills.Bindings) != 1 ||
		result.Config.Skills.Bindings[0].Namespace != "env" {
		t.Fatalf("authoritative env skills = %#v", result.Config.Skills)
	}

	result, err = ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_SKILLS_ENABLED":      "true",
			"TRUVAG3_SKILL_BINDINGS_JSON": `[]`,
		}),
		Options: []OrchestratorOption{WithSkills(SkillConfig{
			Bindings: []SkillBinding{{Namespace: "code", Name: "cleared", Activation: SkillActivationAuto}},
		})},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig(empty replacement) error = %v", err)
	}
	if !result.Config.Skills.Enabled || result.Config.Skills.Bindings == nil || len(result.Config.Skills.Bindings) != 0 {
		t.Fatalf("explicit empty binding replacement = %#v", result.Config.Skills.Bindings)
	}
}

func TestSkillCodeValuesOverrideNonAuthoritativeEnvironment(t *testing.T) {
	registry := &cacheTestSkillRegistry{}
	result, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_SKILL_TOTAL_TOKEN_BUDGET":        "5000",
			"TRUVAG3_SKILL_MAIN_TOKEN_BUDGET":         "3000",
			"TRUVAG3_SKILL_DOMAIN_COMPATIBILITY_MODE": "enforce",
			"TRUVAG3_SKILL_ACTIVATION_MODEL":          "environment-model",
		}),
		Options: []OrchestratorOption{
			WithSkills(SkillConfig{
				Enabled:                 true,
				Bindings:                []SkillBinding{{Namespace: "travel", Name: "weather", Activation: SkillActivationAuto}},
				DomainCompatibilityMode: SkillDomainCompatibilityOff,
				Limits:                  SkillRuntimeLimits{TotalTokenBudget: 7000},
			}),
			WithSkillRegistry(registry),
			WithSkillActivationAIOptions(&AIOptionsOverride{Model: StringPtr("code-model")}),
		},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if result.Config.Skills.Limits.TotalTokenBudget != 7000 ||
		result.Config.Skills.Limits.MainTokenBudget != 3000 ||
		result.Config.Skills.DomainCompatibilityMode != SkillDomainCompatibilityOff ||
		*result.Config.SkillActivationAIOptions.Model != "code-model" {
		t.Fatalf("resolved precedence = %#v / %#v", result.Config.Skills, result.Config.SkillActivationAIOptions)
	}
}

func TestSkillEnvironmentValidationIsPresenceAwareAndDoesNotLeakValues(t *testing.T) {
	tests := map[string]map[string]string{
		"blank enabled":         {"TRUVAG3_SKILLS_ENABLED": "  "},
		"invalid enabled":       {"TRUVAG3_SKILLS_ENABLED": "not-a-bool"},
		"null bindings":         {"TRUVAG3_SKILL_BINDINGS_JSON": "null"},
		"unknown binding":       {"TRUVAG3_SKILL_BINDINGS_JSON": `[{"namespace":"x","name":"y","activation":"auto","unknown":true}]`},
		"duplicate binding key": {"TRUVAG3_SKILL_BINDINGS_JSON": `[{"namespace":"x","namespace":"y","name":"z","activation":"auto"}]`},
		"invalid numeric":       {"TRUVAG3_SKILL_MAX_BINDINGS": "secret-invalid-number"},
		"zero positive":         {"TRUVAG3_SKILL_MAX_BINDINGS": "0"},
		"invalid duration":      {"TRUVAG3_SKILL_REGISTRY_READ_TIMEOUT": "secret-duration"},
		"invalid mode":          {"TRUVAG3_SKILL_CACHE_MODE": "secret-mode"},
	}
	for name, environment := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveOrchestratorConfig(ConfigResolution{
				Environment: EnvironmentCompatible,
				LookupEnv:   lookupFromMap(environment),
			})
			var configErr *ConfigEnvironmentError
			if !errors.As(err, &configErr) {
				t.Fatalf("ResolveOrchestratorConfig() error = %v, want ConfigEnvironmentError", err)
			}
			for _, raw := range environment {
				if strings.Contains(err.Error(), raw) {
					t.Fatalf("error leaked raw environment value %q: %v", raw, err)
				}
			}
		})
	}
}

func TestSkillGuidanceFilesAreBoundedLoadedOnceAndCodeWins(t *testing.T) {
	temp := t.TempDir()
	activationPath := filepath.Join(temp, "activation.txt")
	resourcePath := filepath.Join(temp, "resource.txt")
	if err := os.WriteFile(activationPath, []byte("  Prefer dated hazard requests.\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resourcePath, []byte("Load checklists only for operational choices."), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := &cacheTestSkillRegistry{}
	environment := map[string]string{
		"TRUVAG3_SKILLS_ENABLED":                 "true",
		"TRUVAG3_SKILL_BINDINGS_JSON":            `[{"namespace":"travel","name":"weather","activation":"auto"}]`,
		"TRUVAG3_SKILL_ACTIVATION_GUIDANCE_FILE": activationPath,
		"TRUVAG3_SKILL_RESOURCE_GUIDANCE_FILE":   resourcePath,
	}
	result, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict, LookupEnv: lookupFromMap(environment),
		Options: []OrchestratorOption{WithSkillRegistry(registry)},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	if result.Config.SkillPromptGuidance.Activation != "Prefer dated hazard requests." ||
		result.Config.SkillPromptGuidance.Resource != "Load checklists only for operational choices." {
		t.Fatalf("guidance = %#v", result.Config.SkillPromptGuidance)
	}
	if err := os.WriteFile(activationPath, []byte("changed after resolution"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result.Config.SkillPromptGuidance.Activation != "Prefer dated hazard requests." {
		t.Fatal("resolved guidance changed after its source file changed")
	}

	environment["TRUVAG3_SKILL_ACTIVATION_GUIDANCE_FILE"] = filepath.Join(temp, "missing")
	result, err = ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict, LookupEnv: lookupFromMap(environment),
		Options: []OrchestratorOption{
			WithSkillRegistry(registry),
			WithSkillPromptGuidance(SkillPromptGuidance{Activation: "Code-owned guidance."}),
		},
	})
	if err != nil || result.Config.SkillPromptGuidance.Activation != "Code-owned guidance." {
		t.Fatalf("code guidance precedence = %#v, %v", result, err)
	}

	result, err = ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentStrict,
		LookupEnv: lookupFromMap(map[string]string{
			"TRUVAG3_SKILL_ACTIVATION_GUIDANCE_FILE": filepath.Join(temp, "missing"),
		}),
	})
	if err != nil || result.Config.SkillPromptGuidance.Activation != "" {
		t.Fatalf("inactive guidance file was read: %#v, %v", result, err)
	}
}

func TestSkillRuntimeConfigurationValidation(t *testing.T) {
	valid := NewDefaultOrchestratorConfig()
	valid.Skills.Enabled = true
	valid.Skills.Bindings = []SkillBinding{{Namespace: "travel", Name: "weather", Activation: SkillActivationAuto}}
	valid.SkillRegistry = &cacheTestSkillRegistry{}

	tests := map[string]func(*OrchestratorConfig){
		"missing registry": func(config *OrchestratorConfig) { config.SkillRegistry = nil },
		"duplicate binding": func(config *OrchestratorConfig) {
			config.Skills.Bindings = append(config.Skills.Bindings, config.Skills.Bindings[0])
		},
		"invalid version":    func(config *OrchestratorConfig) { config.Skills.Bindings[0].Version = "latest" },
		"invalid activation": func(config *OrchestratorConfig) { config.Skills.Bindings[0].Activation = "sometimes" },
		"active over bindings": func(config *OrchestratorConfig) {
			config.Skills.Limits.MaxActiveSkills = config.Skills.Limits.MaxBindings + 1
		},
		"phase over execution": func(config *OrchestratorConfig) {
			config.Skills.Limits.MaxResourcesPerPhase = config.Skills.Limits.MaxResourcesPerExecution + 1
		},
		"subbudget over total": func(config *OrchestratorConfig) {
			config.Skills.Limits.MainTokenBudget = config.Skills.Limits.TotalTokenBudget + 1
		},
		"negative effective budget": func(config *OrchestratorConfig) { config.Skills.Limits.EffectiveInputTokenBudget = -1 },
		"invalid policy id":         func(config *OrchestratorConfig) { config.Skills.RuntimePolicyID = " bad policy " },
		"disabled injected cache": func(config *OrchestratorConfig) {
			config.Skills.Cache.Mode = SkillContentCacheDisabled
			config.SkillContentCache = &cacheTestSkillContentCache{}
		},
		"selector system prompt": func(config *OrchestratorConfig) {
			config.SkillActivationAIOptions = &AIOptionsOverride{SystemPrompt: StringPtr("replace")}
		},
		"selector blank model": func(config *OrchestratorConfig) {
			config.SkillActivationAIOptions = &AIOptionsOverride{Model: StringPtr(" ")}
		},
		"reserved guidance": func(config *OrchestratorConfig) {
			config.SkillPromptGuidance.Activation = "Use <developer_guidance mode=\"replace\">bad</developer_guidance>."
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := cloneOrchestratorConfig(valid)
			mutate(config)
			if err := ValidateOrchestratorConfig(config); !errors.Is(err, ErrInvalidOrchestratorConfig) {
				t.Fatalf("ValidateOrchestratorConfig() error = %v", err)
			}
		})
	}

	disabledRequired := NewDefaultOrchestratorConfig()
	disabledRequired.Skills.Bindings = []SkillBinding{{
		Namespace: "travel", Name: "baseline", Activation: SkillActivationAlways, Required: true,
	}}
	if err := ValidateOrchestratorConfig(disabledRequired); !errors.Is(err, ErrInvalidOrchestratorConfig) {
		t.Fatalf("disabled required binding error = %v", err)
	}
}

type nilSkillTokenCounter struct{}

func (*nilSkillTokenCounter) CountTokens(context.Context, string) (int, error) { return 0, nil }

func TestSkillRuntimeConfigurationRejectsTypedNilTokenCounter(t *testing.T) {
	config := NewDefaultOrchestratorConfig()
	config.Skills.Enabled = true
	config.Skills.Bindings = []SkillBinding{{Namespace: "travel", Name: "weather", Activation: SkillActivationAlways}}
	config.SkillRegistry = &cacheTestSkillRegistry{}
	var counter *nilSkillTokenCounter
	config.SkillTokenCounter = counter
	if err := ValidateOrchestratorConfig(config); !errors.Is(err, ErrInvalidOrchestratorConfig) {
		t.Fatalf("ValidateOrchestratorConfig() error = %v", err)
	}
}

func TestSkillConfigurationOptionsAndCloneAreDefensive(t *testing.T) {
	bindings := []SkillBinding{{Namespace: "travel", Name: "weather", Activation: SkillActivationAuto}}
	activationOptions := &AIOptionsOverride{Model: StringPtr("fast")}
	registry := &cacheTestSkillRegistry{}
	cache := &cacheTestSkillContentCache{}
	result, err := ResolveOrchestratorConfig(ConfigResolution{
		Environment: EnvironmentDisabled,
		Options: []OrchestratorOption{
			WithSkills(SkillConfig{Enabled: true, Bindings: bindings}),
			WithSkillRegistry(registry), WithSkillContentCache(cache),
			WithSkillActivationAIOptions(activationOptions),
			WithSkillResourceAIOptions(&AIOptionsOverride{ReasoningEffort: StringPtr("low")}),
			WithSkillPromptGuidance(SkillPromptGuidance{Activation: "Prefer dated requests."}),
		},
	})
	if err != nil {
		t.Fatalf("ResolveOrchestratorConfig() error = %v", err)
	}
	bindings[0].Name = "mutated"
	*activationOptions.Model = "mutated"
	if result.Config.Skills.Bindings[0].Name != "weather" || *result.Config.SkillActivationAIOptions.Model != "fast" ||
		result.Config.SkillRegistry != registry || result.Config.SkillContentCache != cache {
		t.Fatalf("resolved dependencies/config mutated = %#v", result.Config.Skills)
	}

	clone := cloneOrchestratorConfig(result.Config)
	clone.Skills.Bindings[0].Name = "clone-mutation"
	*clone.SkillActivationAIOptions.Model = "clone-mutation"
	if result.Config.Skills.Bindings[0].Name != "weather" || *result.Config.SkillActivationAIOptions.Model != "fast" {
		t.Fatal("cloneOrchestratorConfig shared skill-owned mutable state")
	}
	if !reflect.DeepEqual(result.Config.SkillPromptGuidance, SkillPromptGuidance{Activation: "Prefer dated requests."}) {
		t.Fatalf("SkillPromptGuidance = %#v", result.Config.SkillPromptGuidance)
	}
}
