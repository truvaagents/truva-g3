package orchestration

import (
	"encoding/json"
	"strings"
)

// quoteUnquotedTemplates quotes only structural template values. It never visits
// the contents of a JSON string; application strings are opaque, even in an
// otherwise malformed response. Validation and template binding remain separate.
func quoteUnquotedTemplates(s string) string {
	return repairPlanJSONStructure(s, true)
}

// attemptJSONRepair retains supported leading-marker and trailing-comma recovery.
// Only commas outside strings can be removed.
func attemptJSONRepair(s string) (string, bool) {
	repaired := repairPlanJSONStructure(strings.TrimLeft(s, "\uFEFF\u200B\u200C\u200D"), false)
	return repaired, repaired != s
}

// A type mismatch in valid JSON is not permission to salvage its syntax.
func repairInvalidPlanJSON(s string) (string, bool) {
	if json.Valid([]byte(s)) {
		return s, false
	}
	return attemptJSONRepair(s)
}

// repairPlanJSONStructure scans once, copying quoted tokens byte-for-byte.
// Recovery modes are intentionally independent, preserving parsePlan's order.
func repairPlanJSONStructure(s string, quoteTemplates bool) string {
	var out strings.Builder
	out.Grow(len(s))
	var previous byte
	for i := 0; i < len(s); {
		if s[i] == '"' {
			end := planJSONStringEnd(s, i)
			if end < 0 {
				return s // Unterminated string: no reliable structural boundary.
			}
			out.WriteString(s[i:end])
			i = end
			previous = '"'
			continue
		}
		if quoteTemplates && (previous == ':' || previous == ',' || previous == '[') && strings.HasPrefix(s[i:], "{{") {
			closeAt := strings.Index(s[i+2:], "}}")
			if closeAt < 0 {
				return s
			}
			end := i + 2 + closeAt + 2
			body := s[i+2 : end-2]
			// Ambiguous quotes/escapes/control characters are not repaired.
			if body == "" || strings.ContainsAny(body, "{}\"\\\r\n\t") {
				return s
			}
			out.WriteByte('"')
			out.WriteString(s[i:end])
			out.WriteByte('"')
			i = end
			previous = '"'
			continue
		}
		if !quoteTemplates && s[i] == ',' {
			end := i + 1
			for end < len(s) && planJSONSpace(s[end]) {
				end++
			}
			if end < len(s) && (s[end] == '}' || s[end] == ']') {
				i = end
				continue
			}
		}
		out.WriteByte(s[i])
		if !planJSONSpace(s[i]) {
			previous = s[i]
		}
		i++
	}
	return out.String()
}

func planJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// planJSONStringEnd returns the exclusive closing-quote index, or -1. Full JSON
// escape validation belongs to json.Unmarshal; here escaped quotes are opaque.
func planJSONStringEnd(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// cleanLLMResponse removes an outer prose/fence envelope, not application data.
// Balanced-object extraction ignores braces and Markdown fences inside strings.
// Valid JSON, including non-plan JSON, goes unchanged to typed plan decoding.
func cleanLLMResponse(s string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	// Prefer an explicitly fenced object when the response starts with prose.
	// Once a JSON container/string has started, a fence can be literal payload.
	body := strings.TrimSpace(s)
	if body != "" && !strings.ContainsAny(body[:1], "{[\"") {
		if fence := strings.Index(body, "```"); fence >= 0 {
			candidate := strings.TrimSpace(body[fence+3:])
			if len(candidate) >= 4 && strings.EqualFold(candidate[:4], "json") {
				candidate = strings.TrimSpace(candidate[4:])
			}
			if strings.HasPrefix(candidate, "{") {
				if end := findJSONEndStringSafe(candidate, 0); end >= 0 && strings.HasPrefix(strings.TrimSpace(candidate[end:]), "```") {
					return candidate[:end]
				}
			}
		}
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return s
	}
	end := findJSONEndStringSafe(s, start)
	if end < 0 {
		return s
	}
	return s[start:end]
}
