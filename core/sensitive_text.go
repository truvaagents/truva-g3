package core

import "regexp"

const redactedSensitiveValue = "[REDACTED]"

// Sensitive-value names are deliberately limited to credential-bearing fields.
// Generic names such as "key" and "token" are only treated as sensitive in URL
// query parameters, where they commonly identify credentials and are unlikely to
// be ordinary prose.
const (
	sensitiveValueNamePattern    = `(?:api[_-]?key|apikey|appid|access[_-]?token|refresh[_-]?token|auth[_-]?token|authorization|client[_-]?secret|secret[_-]?(?:key|token)|password|passwd)`
	unquotedSensitiveNamePattern = `(?:api[_-]?key|apikey|appid|access[_-]?token|refresh[_-]?token|auth[_-]?token|client[_-]?secret|secret[_-]?(?:key|token)|password|passwd)`
)

var sensitiveTextPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern:     regexp.MustCompile(`(?i)("` + sensitiveValueNamePattern + `"\s*:\s*")([^"]*)(")`),
		replacement: `${1}` + redactedSensitiveValue + `${3}`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)('` + sensitiveValueNamePattern + `'\s*:\s*')([^']*)(')`),
		replacement: `${1}` + redactedSensitiveValue + `${3}`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\bauthorization\b\s*[=:]\s*)(?:"[^"]*"|'[^']*'|(?:bearer|basic)\s+[^\s,;}&\]]+|[^\s,;}&\]]+)`),
		replacement: `${1}` + redactedSensitiveValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b` + unquotedSensitiveNamePattern + `\b\s*[=:]\s*")([^"]*)(")`),
		replacement: `${1}` + redactedSensitiveValue + `${3}`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b` + unquotedSensitiveNamePattern + `\b\s*[=:]\s*')([^']*)(')`),
		replacement: `${1}` + redactedSensitiveValue + `${3}`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b` + unquotedSensitiveNamePattern + `\b\s*[=:]\s*)([^\s,;}&\]"']+)`),
		replacement: `${1}` + redactedSensitiveValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)([?&](?:` + sensitiveValueNamePattern + `|key|token)=)([^&#\s]*)`),
		replacement: `${1}` + redactedSensitiveValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)([A-Za-z0-9._~+/=-]+)`),
		replacement: `${1}` + redactedSensitiveValue,
	},
	{
		pattern:     regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/@\s]+)@`),
		replacement: `${1}` + redactedSensitiveValue + `@`,
	},
}

// RedactSensitiveText removes common credential values from untrusted text while
// retaining enough surrounding structure for diagnostics. It is intended for
// error messages, external log lines, traces, and other observability surfaces;
// callers must not treat pattern-based redaction as a substitute for avoiding
// secret-bearing payloads in the first place.
//
// The function is deterministic, does not inspect environment variables, and
// returns an empty string for an empty input.
func RedactSensitiveText(value string) string {
	for _, candidate := range sensitiveTextPatterns {
		value = candidate.pattern.ReplaceAllString(value, candidate.replacement)
	}
	return value
}

type redactedSensitiveError struct {
	cause error
}

func (e *redactedSensitiveError) Error() string {
	return RedactSensitiveText(e.cause.Error())
}

func (e *redactedSensitiveError) Unwrap() error {
	return e.cause
}

// RedactSensitiveError returns an error whose observable message has common
// credential values removed while preserving the original error for
// errors.Is/errors.As control flow. It returns nil when err is nil.
func RedactSensitiveError(err error) error {
	if err == nil {
		return nil
	}
	return &redactedSensitiveError{cause: err}
}
