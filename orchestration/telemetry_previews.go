package orchestration

import "unicode/utf8"

// truncateTelemetryPreview bounds an existing derived span preview by prefix
// bytes plus the existing "..." suffix. For valid UTF-8 source text, truncation
// ends at a rune boundary so OTLP protobuf encoding cannot reject an entire
// export batch because the preview split a character.
//
// This helper is only for telemetry previews. It does not change raw payloads,
// debug records, model prompts or the legacy truncateString contract. It does
// not sanitize source text that was already invalid UTF-8.
func truncateTelemetryPreview(value string, maxPrefixBytes int) string {
	maxPrefixBytes = max(0, maxPrefixBytes)
	if len(value) <= maxPrefixBytes {
		return value
	}
	end := maxPrefixBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}
