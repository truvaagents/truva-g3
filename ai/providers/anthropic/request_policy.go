package anthropic

import "strings"

type samplingPolicy uint8

const (
	samplingUnknown samplingPolicy = iota
	samplingAllowed
	samplingOmitted
)

var omitSamplingPrefixes = []string{
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-sonnet-5",
	"claude-fable-5",
	"claude-mythos-5",
	"claude-mythos-preview",
}

func samplingPolicyForModel(model string) samplingPolicy {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range omitSamplingPrefixes {
		if modelInFamily(normalized, prefix) {
			return samplingOmitted
		}
	}
	if modelInFamily(normalized, "claude-sonnet-4-6") ||
		modelInFamily(normalized, "claude-haiku-4-5") {
		return samplingAllowed
	}
	return samplingUnknown
}

func modelInFamily(normalizedModel, normalizedFamily string) bool {
	return normalizedModel == normalizedFamily ||
		strings.HasPrefix(normalizedModel, normalizedFamily+"-")
}

func deleteKeyFold(body map[string]interface{}, names ...string) []string {
	removed := make([]string, 0, len(names))
	for _, name := range names {
		found := false
		for key := range body {
			if strings.EqualFold(key, name) {
				delete(body, key)
				found = true
			}
		}
		if found {
			removed = append(removed, "/"+name)
		}
	}
	return removed
}

func (policy samplingPolicy) String() string {
	switch policy {
	case samplingAllowed:
		return "allowed"
	case samplingOmitted:
		return "omitted"
	default:
		return "unknown"
	}
}
