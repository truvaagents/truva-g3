// Package modelcatalog owns the side-effect-free OpenAI-compatible semantic
// model alias catalog without depending on the OpenAI client or factory.
package modelcatalog

import (
	"os"
	"strings"
)

var defaultAliases = map[string]map[string]string{
	"openai": {
		"fast": "gpt-4.1-mini", "smart": "o3", "vision": "gpt-4.1",
		"code": "o3", "default": "gpt-4.1-mini",
	},
	"openai.deepseek": {
		"fast": "deepseek-chat", "smart": "deepseek-reasoner",
		"code": "deepseek-chat", "default": "deepseek-chat",
	},
	"openai.groq": {
		"fast": "llama-3.1-8b-instant", "smart": "openai/gpt-oss-120b",
		"code": "openai/gpt-oss-120b", "default": "openai/gpt-oss-120b",
	},
	"openai.together": {
		"fast": "meta-llama/Llama-3.1-8B-Instruct-Turbo", "smart": "meta-llama/Llama-3.3-70B-Instruct-Turbo",
		"code": "Qwen/Qwen2.5-Coder-32B-Instruct", "default": "meta-llama/Llama-3.3-70B-Instruct-Turbo",
	},
	"openai.xai": {
		"fast": "grok-2", "smart": "grok-3-beta", "code": "grok-3-mini-beta",
		"vision": "grok-2-vision-latest", "default": "grok-3-beta",
	},
	"openai.mistral": {
		"fast": "mistral-small-latest", "smart": "mistral-large-latest",
		"code": "codestral-latest", "default": "mistral-medium-latest",
	},
	"openai.qwen": {
		"fast": "qwen-turbo", "smart": "qwen-max", "code": "qwen3-coder-plus", "default": "qwen-plus",
	},
	"openai.ollama": {
		"fast": "llama3.2:1b", "smart": "llama3.2", "code": "codellama", "default": "llama3.2",
	},
}

// DefaultAliases returns an isolated copy of the built-in catalog.
func DefaultAliases() map[string]map[string]string {
	return cloneAliases(defaultAliases)
}

func cloneAliases(source map[string]map[string]string) map[string]map[string]string {
	clone := make(map[string]map[string]string, len(source))
	for providerAlias, aliases := range source {
		providerClone := make(map[string]string, len(aliases))
		for alias, model := range aliases {
			providerClone[alias] = model
		}
		clone[providerAlias] = providerClone
	}
	return clone
}

// Resolve applies environment overrides and the immutable built-in catalog.
func Resolve(providerAlias, model string) string {
	return ResolveWithAliases(defaultAliases, providerAlias, model)
}

// ResolveWithAliases applies environment overrides and the supplied catalog.
func ResolveWithAliases(aliases map[string]map[string]string, providerAlias, model string) string {
	if providerAlias == "" {
		providerAlias = "openai"
	}
	envProvider := strings.TrimPrefix(providerAlias, "openai.")
	envKey := "TRUVAG3_" + strings.ToUpper(envProvider) + "_MODEL_" + strings.ToUpper(model)
	if override := os.Getenv(envKey); override != "" {
		return override
	}
	if providerAliases, ok := aliases[providerAlias]; ok {
		if resolved, ok := providerAliases[model]; ok {
			return resolved
		}
	}
	return model
}
