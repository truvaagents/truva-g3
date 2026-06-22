package main

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// moreCapabilitySpecs is the fan-out batch (ANALYSIS.md §14) — the remaining Tier-1/Tier-2
// capabilities, each a spec function + prompt over the same contained engine.
func moreCapabilitySpecs() []capabilitySpec {
	return []capabilitySpec{
		transformDataSpec(),
		convertFormatSpec(),
		parseLogsSpec(),
		synthesizeRegexSpec(),
		generateTestsSpec(),
		redactPIISpec(),
		scanSecretsSpec(),
		reviewConfigSpec(),
	}
}

// timeoutHint is the shared optional timeout field hint.
func timeoutHint() core.FieldHint {
	return core.FieldHint{Name: "timeout_seconds", Type: "number", Example: "300", Description: "Transaction time budget; clamped to the adapter's ceiling"}
}

// ---- transform_data ----

func transformDataSpec() capabilitySpec {
	return capabilitySpec{
		Name: "transform_data",
		Description: "Transform a dataset by running code in a sandbox: filter, pivot, reshape, join, or aggregate it, returning the transformed output. " +
			"Provide the data inline plus a plain-language instruction; the agent writes the data to a file, runs a script to apply the transformation, and returns the result. " +
			"Use for ETL-style reshaping where a deterministic, executed transform beats a hand-written guess. " +
			"Required: data, instruction. Optional: in_format, out_format, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "data", Type: "string", Example: "day,sales\\nmon,5\\ntue,9", Description: "The data to transform, inline"},
				{Name: "instruction", Type: "string", Example: "keep only rows where sales > 6 and sort descending by sales", Description: "Plain-language description of the transformation to apply"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "in_format", Type: "string", Example: "csv", Description: "Source format hint (csv, json, tsv, ...)"},
				{Name: "out_format", Type: "string", Example: "json", Description: "Desired output format"},
				timeoutHint(),
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "result", Type: "string", Description: "The transformed data"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "out_format", Type: "string", Description: "The format of the result"},
			},
		},
		build: transformDataBuild,
		parse: transformDataParse,
	}
}

// TransformDataRequest is the input for transform_data.
type TransformDataRequest struct {
	Data        string `json:"data"`
	Instruction string `json:"instruction"`
	InFormat    string `json:"in_format,omitempty"`
	OutFormat   string `json:"out_format,omitempty"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// TransformDataResponse is the output of transform_data.
type TransformDataResponse struct {
	Result    string `json:"result"`
	OutFormat string `json:"out_format,omitempty"`
}

func transformDataBuild(raw []byte) (string, int, int, error) {
	var req TransformDataRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Data = strings.TrimSpace(req.Data)
	req.Instruction = strings.TrimSpace(req.Instruction)
	if req.Data == "" {
		return "", 0, 0, errors.New("data is required")
	}
	if req.Instruction == "" {
		return "", 0, 0, errors.New("instruction is required")
	}

	var b strings.Builder
	b.WriteString("You are a data-transformation process in a sandbox with shell and file tools. ")
	b.WriteString("Write the DATA below to a file, then apply the TRANSFORMATION by running a script — do not hand-edit the output. ")
	if of := strings.TrimSpace(req.OutFormat); of != "" {
		b.WriteString("Produce the result in " + of + " format. ")
	}
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"result": string, "out_format": string}`)
	b.WriteString(" where result is the transformed data and out_format names its format. No text outside the JSON.\n\n")
	b.WriteString("--- TRANSFORMATION ---\n")
	b.WriteString(req.Instruction)
	b.WriteString("\n\n--- DATA")
	if f := strings.TrimSpace(req.InFormat); f != "" {
		b.WriteString(" (" + f + ")")
	}
	b.WriteString(" ---\n")
	b.WriteString(req.Data)
	return b.String(), req.TimeoutSecs, len(req.Data), nil
}

func transformDataParse(output string) interface{} {
	var resp TransformDataResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.Result != "" {
		return resp
	}
	return TransformDataResponse{Result: strings.TrimSpace(output)}
}

// ---- convert_format ----

func convertFormatSpec() capabilitySpec {
	return capabilitySpec{
		Name: "convert_format",
		Description: "Convert structured data from one format to another (YAML, JSON, TOML, CSV) using tools, so the conversion is lossless and correct. " +
			"Provide the data inline with its source and target formats; the agent converts it by running a parser rather than guessing token by token. " +
			"Use when you need a reliable format change, for example Helm values from YAML to JSON. " +
			"Required: data, from, to. Optional: timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "data", Type: "string", Example: "name: app\\nport: 8080", Description: "The data to convert, inline"},
				{Name: "from", Type: "string", Example: "yaml", Description: "Source format (yaml, json, toml, csv)"},
				{Name: "to", Type: "string", Example: "json", Description: "Target format (yaml, json, toml, csv)"},
			},
			OptionalFields: []core.FieldHint{timeoutHint()},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "result", Type: "string", Description: "The converted data in the target format"},
			},
		},
		build: convertFormatBuild,
		parse: convertFormatParse,
	}
}

// ConvertFormatRequest is the input for convert_format.
type ConvertFormatRequest struct {
	Data        string `json:"data"`
	From        string `json:"from"`
	To          string `json:"to"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// ConvertFormatResponse is the output of convert_format.
type ConvertFormatResponse struct {
	Result string `json:"result"`
}

func convertFormatBuild(raw []byte) (string, int, int, error) {
	var req ConvertFormatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Data = strings.TrimSpace(req.Data)
	req.From = strings.TrimSpace(req.From)
	req.To = strings.TrimSpace(req.To)
	if req.Data == "" {
		return "", 0, 0, errors.New("data is required")
	}
	if req.From == "" || req.To == "" {
		return "", 0, 0, errors.New("from and to formats are required")
	}

	var b strings.Builder
	b.WriteString("You are a format-conversion process in a sandbox with shell and file tools. ")
	b.WriteString("Convert the DATA below from " + req.From + " to " + req.To + " by running a real parser/serializer, losslessly. ")
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"result": string}`)
	b.WriteString(" where result is the converted data in " + req.To + " format. No text outside the JSON.\n\n")
	b.WriteString("--- DATA (" + req.From + ") ---\n")
	b.WriteString(req.Data)
	return b.String(), req.TimeoutSecs, len(req.Data), nil
}

func convertFormatParse(output string) interface{} {
	var resp ConvertFormatResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.Result != "" {
		return resp
	}
	return ConvertFormatResponse{Result: strings.TrimSpace(output)}
}

// ---- parse_logs ----

func parseLogsSpec() capabilitySpec {
	return capabilitySpec{
		Name: "parse_logs",
		Description: "Analyze a block of logs by running code in a sandbox: classify errors, build a failure timeline, identify the likely root cause, and rank the top offenders. " +
			"Provide the logs inline; the agent writes them to a file and computes the counts and patterns rather than eyeballing them. " +
			"Use for incident triage and log forensics where accurate counts and ordering matter. " +
			"Required: logs. Optional: focus, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "logs", Type: "string", Example: "2026-06-22T10:00:01 ERROR db timeout\\n2026-06-22T10:00:02 ERROR db timeout", Description: "The raw log text to analyze; may be large"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "focus", Type: "string", Example: "database errors", Description: "Optional aspect to emphasize"},
				timeoutHint(),
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "error_classes", Type: "array", Description: "Distinct error patterns with counts: {pattern, count}"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "timeline", Type: "string", Description: "A brief chronology of notable events"},
				{Name: "likely_root_cause", Type: "string", Description: "The most probable root cause"},
				{Name: "top_offenders", Type: "array", Description: "The most frequent error lines or sources"},
			},
		},
		build: parseLogsBuild,
		parse: parseLogsParse,
	}
}

// ParseLogsRequest is the input for parse_logs.
type ParseLogsRequest struct {
	Logs        string `json:"logs"`
	Focus       string `json:"focus,omitempty"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// LogErrorClass is a distinct error pattern and how often it occurred.
type LogErrorClass struct {
	Pattern string `json:"pattern"`
	Count   int    `json:"count"`
}

// ParseLogsResponse is the output of parse_logs.
type ParseLogsResponse struct {
	ErrorClasses    []LogErrorClass `json:"error_classes"`
	Timeline        string          `json:"timeline,omitempty"`
	LikelyRootCause string          `json:"likely_root_cause,omitempty"`
	TopOffenders    []string        `json:"top_offenders,omitempty"`
}

func parseLogsBuild(raw []byte) (string, int, int, error) {
	var req ParseLogsRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Logs = strings.TrimSpace(req.Logs)
	if req.Logs == "" {
		return "", 0, 0, errors.New("logs is required")
	}

	var b strings.Builder
	b.WriteString("You are a log-analysis process in a sandbox with shell and file tools. ")
	b.WriteString("Write the LOGS below to a file, then analyze them by running code (grep/awk/sort/uniq/etc.) — count, do not estimate. ")
	if f := strings.TrimSpace(req.Focus); f != "" {
		b.WriteString("Focus on: " + f + ". ")
	}
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"error_classes": [{"pattern": string, "count": number}], "timeline": string, "likely_root_cause": string, "top_offenders": [string]}`)
	b.WriteString(". error_classes groups similar errors with real counts; top_offenders are the most frequent lines or sources. No text outside the JSON.\n\n")
	b.WriteString("--- LOGS ---\n")
	b.WriteString(req.Logs)
	return b.String(), req.TimeoutSecs, len(req.Logs), nil
}

func parseLogsParse(output string) interface{} {
	var resp ParseLogsResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil &&
		(resp.ErrorClasses != nil || resp.LikelyRootCause != "" || resp.Timeline != "") {
		return resp
	}
	return ParseLogsResponse{LikelyRootCause: strings.TrimSpace(output)}
}

// ---- synthesize_regex ----

func synthesizeRegexSpec() capabilitySpec {
	return capabilitySpec{
		Name: "synthesize_regex",
		Description: "Synthesize a regular expression from positive and negative examples and test it against them before returning, so you get a checked pattern instead of a guess. " +
			"Provide strings that should match and strings that should not; the agent derives a regex, runs it against every example, and reports any that fail. " +
			"Use to build a reliable pattern from samples, for example matching a set of product codes. " +
			"Required: should_match. Optional: should_not_match, flavor, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "should_match", Type: "array", Example: `["AB-12","CD-34"]`, Description: "Strings the regex must match"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "should_not_match", Type: "array", Example: `["AB12","xyz"]`, Description: "Strings the regex must NOT match"},
				{Name: "flavor", Type: "string", Example: "pcre", Description: "Regex flavor (pcre, re2, python, ...)"},
				timeoutHint(),
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "regex", Type: "string", Description: "The synthesized regular expression"},
				{Name: "passed", Type: "boolean", Description: "True if the regex satisfied all provided examples"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "failures", Type: "array", Description: "Examples the regex classified incorrectly, if any"},
			},
		},
		build: synthesizeRegexBuild,
		parse: synthesizeRegexParse,
	}
}

// SynthesizeRegexRequest is the input for synthesize_regex.
type SynthesizeRegexRequest struct {
	ShouldMatch    []string `json:"should_match"`
	ShouldNotMatch []string `json:"should_not_match,omitempty"`
	Flavor         string   `json:"flavor,omitempty"`
	TimeoutSecs    int      `json:"timeout_seconds,omitempty"`
}

// SynthesizeRegexResponse is the output of synthesize_regex.
type SynthesizeRegexResponse struct {
	Regex    string   `json:"regex"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

func synthesizeRegexBuild(raw []byte) (string, int, int, error) {
	var req SynthesizeRegexRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	if len(req.ShouldMatch) == 0 {
		return "", 0, 0, errors.New("should_match is required (at least one example)")
	}

	chars := 0
	for _, s := range req.ShouldMatch {
		chars += len(s)
	}
	for _, s := range req.ShouldNotMatch {
		chars += len(s)
	}

	flavor := strings.TrimSpace(req.Flavor)
	if flavor == "" {
		flavor = "PCRE"
	}
	mustMatch, _ := json.Marshal(req.ShouldMatch)
	mustNot, _ := json.Marshal(req.ShouldNotMatch)

	var b strings.Builder
	b.WriteString("You are a regex-synthesis process in a sandbox with shell and file tools. ")
	b.WriteString("Derive a single " + flavor + " regular expression that matches every string in SHOULD_MATCH and none in SHOULD_NOT_MATCH. ")
	b.WriteString("TEST your candidate against every example by running code, and refine until it passes or you cannot improve it. ")
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"regex": string, "passed": boolean, "failures": [string]}`)
	b.WriteString(" where passed is true only if all examples are satisfied and failures lists any example the regex got wrong. No text outside the JSON.\n\n")
	b.WriteString("--- SHOULD_MATCH ---\n")
	b.Write(mustMatch)
	b.WriteString("\n\n--- SHOULD_NOT_MATCH ---\n")
	b.Write(mustNot)
	return b.String(), req.TimeoutSecs, chars, nil
}

func synthesizeRegexParse(output string) interface{} {
	var resp SynthesizeRegexResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.Regex != "" {
		return resp
	}
	return SynthesizeRegexResponse{Regex: strings.TrimSpace(output)}
}

// ---- generate_tests ----

func generateTestsSpec() capabilitySpec {
	return capabilitySpec{
		Name: "generate_tests",
		Description: "Generate unit tests for a function or file, and run them when the language toolchain is available so they are known to pass. " +
			"Provide the code; the agent writes tests, executes them if it can, and returns the test source plus any run results. " +
			"Use to bootstrap test coverage for a snippet or module. " +
			"Required: code. Optional: language, framework, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "code", Type: "string", Example: "def add(a, b):\\n    return a + b", Description: "The function or file to generate tests for"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "language", Type: "string", Example: "python", Description: "Language hint"},
				{Name: "framework", Type: "string", Example: "pytest", Description: "Preferred test framework"},
				timeoutHint(),
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "tests", Type: "string", Description: "The generated test source code"},
				{Name: "ran", Type: "boolean", Description: "Whether the tests were executed in the sandbox"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "results", Type: "string", Description: "Test run output, if executed"},
			},
		},
		build: generateTestsBuild,
		parse: generateTestsParse,
	}
}

// GenerateTestsRequest is the input for generate_tests.
type GenerateTestsRequest struct {
	Code        string `json:"code"`
	Language    string `json:"language,omitempty"`
	Framework   string `json:"framework,omitempty"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// GenerateTestsResponse is the output of generate_tests.
type GenerateTestsResponse struct {
	Tests   string `json:"tests"`
	Ran     bool   `json:"ran"`
	Results string `json:"results,omitempty"`
}

func generateTestsBuild(raw []byte) (string, int, int, error) {
	var req GenerateTestsRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		return "", 0, 0, errors.New("code is required")
	}

	var b strings.Builder
	b.WriteString("You are a test-generation process in a sandbox with shell and file tools. ")
	b.WriteString("Write thorough unit tests for the CODE below")
	if lang := strings.TrimSpace(req.Language); lang != "" {
		b.WriteString(" in " + lang)
	}
	if fw := strings.TrimSpace(req.Framework); fw != "" {
		b.WriteString(" using " + fw)
	}
	b.WriteString(", covering edge cases. If the toolchain is available, run the tests and capture the result. ")
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"tests": string, "ran": boolean, "results": string}`)
	b.WriteString(" where tests is the test source, ran indicates whether you executed them, and results is the run output (or \"\"). No text outside the JSON.\n\n")
	b.WriteString("--- CODE ---\n")
	b.WriteString(req.Code)
	return b.String(), req.TimeoutSecs, len(req.Code), nil
}

func generateTestsParse(output string) interface{} {
	var resp GenerateTestsResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.Tests != "" {
		return resp
	}
	return GenerateTestsResponse{Tests: strings.TrimSpace(output)}
}

// ---- redact_pii ----

func redactPIISpec() capabilitySpec {
	return capabilitySpec{
		Name: "redact_pii",
		Description: "Find and mask personally identifiable information in text, code, or config, returning the redacted text plus a summary of what was found. " +
			"Provide the text; the agent detects PII such as emails, phone numbers, and identifiers and replaces them with a mask. " +
			"Use to scrub data before logging or sharing. " +
			"Required: text. Optional: categories, mask, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "text", Type: "string", Example: "Email john@acme.io or call 555-0100.", Description: "The text to scan and redact"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "categories", Type: "array", Example: `["email","phone"]`, Description: "Restrict to specific PII categories"},
				{Name: "mask", Type: "string", Example: "[REDACTED]", Description: "Replacement token (default [REDACTED])"},
				timeoutHint(),
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "redacted_text", Type: "string", Description: "The text with PII masked"},
				{Name: "findings", Type: "array", Description: "What was found, as {type, count} (no raw values)"},
			},
		},
		build: redactPIIBuild,
		parse: redactPIIParse,
	}
}

// RedactPIIRequest is the input for redact_pii.
type RedactPIIRequest struct {
	Text        string   `json:"text"`
	Categories  []string `json:"categories,omitempty"`
	Mask        string   `json:"mask,omitempty"`
	TimeoutSecs int      `json:"timeout_seconds,omitempty"`
}

// PIIFinding reports a category of PII found and how many, without echoing the value.
type PIIFinding struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// RedactPIIResponse is the output of redact_pii.
type RedactPIIResponse struct {
	RedactedText string       `json:"redacted_text"`
	Findings     []PIIFinding `json:"findings"`
}

func redactPIIBuild(raw []byte) (string, int, int, error) {
	var req RedactPIIRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return "", 0, 0, errors.New("text is required")
	}
	mask := strings.TrimSpace(req.Mask)
	if mask == "" {
		mask = "[REDACTED]"
	}

	var b strings.Builder
	b.WriteString("You are a PII-redaction process. Find personally identifiable information in the TEXT below and replace each occurrence with the mask \"" + mask + "\". ")
	if len(req.Categories) > 0 {
		cats, _ := json.Marshal(req.Categories)
		b.WriteString("Restrict to these categories: " + string(cats) + ". ")
	}
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"redacted_text": string, "findings": [{"type": string, "count": number}]}`)
	b.WriteString(". findings summarizes what was found by category and count — do NOT include the actual PII values. No text outside the JSON.\n\n")
	b.WriteString("--- TEXT ---\n")
	b.WriteString(req.Text)
	return b.String(), req.TimeoutSecs, len(req.Text), nil
}

func redactPIIParse(output string) interface{} {
	var resp RedactPIIResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.RedactedText != "" {
		return resp
	}
	return RedactPIIResponse{RedactedText: strings.TrimSpace(output), Findings: []PIIFinding{}}
}

// ---- scan_secrets ----

func scanSecretsSpec() capabilitySpec {
	return capabilitySpec{
		Name: "scan_secrets",
		Description: "Scan code or configuration for hardcoded secrets such as API keys, tokens, and passwords, returning the findings with their type, location, and severity. " +
			"Provide the content; the agent looks for secret-like patterns and high-entropy strings and reports them. " +
			"Use as a pre-commit or review check. " +
			"Required: content. Optional: timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "content", Type: "string", Example: "config or source that may contain a hardcoded key, token, or password", Description: "The code or config to scan"},
			},
			OptionalFields: []core.FieldHint{timeoutHint()},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "findings", Type: "array", Description: "Secrets found: {type, location, severity, match} (match is masked)"},
				{Name: "parsed", Type: "boolean", Description: "True if the scan output parsed cleanly; if false an empty findings list is NOT a verified-clean result"},
			},
		},
		build: scanSecretsBuild,
		parse: scanSecretsParse,
	}
}

// ScanSecretsRequest is the input for scan_secrets.
type ScanSecretsRequest struct {
	Content     string `json:"content"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// SecretFinding reports a detected secret. Match holds the secret value; the adapter masks it
// (maskSecret) before returning, so a full secret is never echoed even if the model does not mask.
type SecretFinding struct {
	Type     string `json:"type"`
	Location string `json:"location,omitempty"`
	Severity string `json:"severity"`
	Match    string `json:"match,omitempty"`
}

// ScanSecretsResponse is the output of scan_secrets.
type ScanSecretsResponse struct {
	Findings []SecretFinding `json:"findings"`
	Parsed   bool            `json:"parsed"` // false = unparseable model output; an empty findings list is NOT a verified-clean result
}

func scanSecretsBuild(raw []byte) (string, int, int, error) {
	var req ScanSecretsRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		return "", 0, 0, errors.New("content is required")
	}

	var b strings.Builder
	b.WriteString("You are a secret-scanning process. Inspect the CONTENT below for hardcoded secrets: API keys, tokens, passwords, private keys, connection strings, and high-entropy values. ")
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"findings": [{"type": string, "location": string, "severity": "critical|high|medium|low", "match": string}]}`)
	b.WriteString(". location is a line reference if known; match is the exact secret value you found (the adapter masks it before returning). Return an empty findings array if none are found. No text outside the JSON.\n\n")
	b.WriteString("--- CONTENT ---\n")
	b.WriteString(req.Content)
	return b.String(), req.TimeoutSecs, len(req.Content), nil
}

func scanSecretsParse(output string) interface{} {
	var resp ScanSecretsResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.Findings != nil {
		maskSecretFindings(resp.Findings)
		resp.Parsed = true
		return resp
	}
	if arr := extractJSONArray(output); arr != "" {
		var fs []SecretFinding
		if err := json.Unmarshal([]byte(arr), &fs); err == nil {
			maskSecretFindings(fs)
			return ScanSecretsResponse{Findings: fs, Parsed: true}
		}
	}
	return ScanSecretsResponse{Findings: []SecretFinding{}, Parsed: false}
}

// maskSecretFindings masks each finding's secret value so a full secret is never returned — a
// Go-side guarantee independent of whether the model honored the masking instruction.
func maskSecretFindings(fs []SecretFinding) {
	for i := range fs {
		fs[i].Match = maskSecret(fs[i].Match)
	}
}

// maskSecret shows a few leading characters of a secret and masks the rest.
func maskSecret(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) == 0 {
		return ""
	}
	if len(r) <= 4 {
		return "****"
	}
	keep := 4
	if len(r) < 8 {
		keep = 2
	}
	return string(r[:keep]) + "…"
}

// ---- review_config ----

func reviewConfigSpec() capabilitySpec {
	return capabilitySpec{
		Name: "review_config",
		Description: "Review infrastructure-as-code or configuration (Dockerfile, Kubernetes manifest, Terraform) for misconfigurations and risky defaults, returning structured findings. " +
			"Provide the content and optionally its kind; the agent inspects it, can run available linters, and reports issues with a rule, location, and suggested fix. " +
			"Use to catch insecure or fragile configuration before it ships. " +
			"Required: content. Optional: kind, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "content", Type: "string", Example: "FROM ubuntu:latest\\nUSER root", Description: "The config or IaC content to review"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "kind", Type: "string", Example: "dockerfile", Description: "Config kind (dockerfile, kubernetes, terraform, ...)"},
				timeoutHint(),
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "findings", Type: "array", Description: "Issues: {severity, rule, location, claim, suggestion}"},
			},
		},
		build: reviewConfigBuild,
		parse: reviewConfigParse,
	}
}

// ReviewConfigRequest is the input for review_config.
type ReviewConfigRequest struct {
	Content     string `json:"content"`
	Kind        string `json:"kind,omitempty"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// ConfigFinding reports a configuration issue.
type ConfigFinding struct {
	Severity   string `json:"severity"`
	Rule       string `json:"rule,omitempty"`
	Location   string `json:"location,omitempty"`
	Claim      string `json:"claim"`
	Suggestion string `json:"suggestion,omitempty"`
}

// ReviewConfigResponse is the output of review_config.
type ReviewConfigResponse struct {
	Findings []ConfigFinding `json:"findings"`
}

func reviewConfigBuild(raw []byte) (string, int, int, error) {
	var req ReviewConfigRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		return "", 0, 0, errors.New("content is required")
	}

	var b strings.Builder
	b.WriteString("You are a configuration-review process. Review the CONFIG below for misconfigurations, insecure defaults, and fragile settings")
	if k := strings.TrimSpace(req.Kind); k != "" {
		b.WriteString(" (kind: " + k + ")")
	}
	b.WriteString(". You may run available linters. ")
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"findings": [{"severity": "critical|high|medium|low|info", "rule": string, "location": string, "claim": string, "suggestion": string}]}`)
	b.WriteString(" where claim states the issue and suggestion is the fix. Return an empty findings array if there are no issues. No text outside the JSON.\n\n")
	b.WriteString("--- CONFIG ---\n")
	b.WriteString(req.Content)
	return b.String(), req.TimeoutSecs, len(req.Content), nil
}

func reviewConfigParse(output string) interface{} {
	var resp ReviewConfigResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.Findings != nil {
		return resp
	}
	if arr := extractJSONArray(output); arr != "" {
		var fs []ConfigFinding
		if err := json.Unmarshal([]byte(arr), &fs); err == nil {
			return ReviewConfigResponse{Findings: fs}
		}
	}
	return ReviewConfigResponse{Findings: []ConfigFinding{}}
}
