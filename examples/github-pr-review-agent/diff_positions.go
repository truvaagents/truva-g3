package main

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

// validPosition is a single (line, side) pair that GitHub will accept as the
// target of an inline review comment.
type validPosition struct {
	Line int
	Side string // "RIGHT" (added/context lines, indexed in new file) or "LEFT" (deleted/context lines, indexed in old file)
}

// hunkHeaderRe matches unified-diff hunk headers:
//
//	@@ -oldStart,oldLen +newStart,newLen @@ optional context
//
// Both ",oldLen" and ",newLen" are optional (omitted when 1).
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseValidPositions walks a unified-diff patch and returns the set of
// (line, side) pairs that GitHub allows inline comments on. For each hunk:
//
//   - lines starting with '+' are commentable as RIGHT (line in new file)
//   - lines starting with '-' are commentable as LEFT (line in old file)
//   - context lines starting with ' ' are commentable on either side
//   - lines starting with '\' (e.g. "\ No newline at end of file") are skipped
//
// Lines outside any hunk (the "diff --git ..." headers, "+++ b/path" lines,
// etc.) are not commentable and are excluded by the in-hunk gate.
//
// This is the agent-side equivalent of the MapToGitHubDiffPosition helper
// originally sketched in AGENT_PLAN.md but never implemented in earlier
// passes — flagged by the developer review.
func parseValidPositions(patch string) map[validPosition]bool {
	out := map[validPosition]bool{}
	if patch == "" {
		return out
	}

	scanner := bufio.NewScanner(strings.NewReader(patch))
	// A single source line with macros / minified code can exceed bufio's
	// default 64 KiB token buffer; raise to 1 MiB to match
	// extractLineWindow's bounds.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentNew, currentOld int
	inHunk := false

	for scanner.Scan() {
		line := scanner.Text()
		if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
			currentOld, _ = strconv.Atoi(m[1])
			currentNew, _ = strconv.Atoi(m[2])
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		if line == "" {
			// Blank line within a hunk — treat as context (advances both sides).
			out[validPosition{Line: currentNew, Side: "RIGHT"}] = true
			out[validPosition{Line: currentOld, Side: "LEFT"}] = true
			currentNew++
			currentOld++
			continue
		}
		switch line[0] {
		case '+':
			out[validPosition{Line: currentNew, Side: "RIGHT"}] = true
			currentNew++
		case '-':
			out[validPosition{Line: currentOld, Side: "LEFT"}] = true
			currentOld++
		case ' ':
			out[validPosition{Line: currentNew, Side: "RIGHT"}] = true
			out[validPosition{Line: currentOld, Side: "LEFT"}] = true
			currentNew++
			currentOld++
		case '\\':
			// "\ No newline at end of file" — annotation, no line consumed.
		default:
			// Anything else (file headers, index lines that slipped past, etc.)
			// — exit hunk mode until we see another @@ header.
			inHunk = false
		}
	}
	return out
}

// findingMappable returns true if the finding's (Path, Line, Side) is a
// position GitHub will accept for an inline review comment, given a
// pre-parsed valid-position set for that path.
func findingMappable(f ReviewFinding, valid map[validPosition]bool) bool {
	if len(valid) == 0 {
		return false
	}
	side := f.Side
	if side == "" {
		side = "RIGHT" // default convention; matches verifyEvidence's fallback
	}
	return valid[validPosition{Line: f.Line, Side: side}]
}

// extractPatchContext returns a multi-line snippet from a unified-diff patch
// containing source lines near the given (line, side) position. Used to
// verify finding evidence:
//
//   - For side="RIGHT" at newLine N: includes added (+) and context ( ) lines
//     where the new-file line is within [N-radius, N+radius]. (Used as a
//     fallback when the head-file artifact isn't available.)
//   - For side="LEFT" at oldLine N: includes deleted (-) and context ( ) lines
//     where the old-file line is within [N-radius, N+radius]. (Used as the
//     primary verification path — head-file artifact doesn't contain deleted
//     code, so LEFT-side findings would otherwise drop as ungrounded.)
//
// Diff markers (+/-/space) are stripped from the output; the snippet contains
// only source content so claimGroundedInContext can do straight substring
// matching against the model's quoted evidence.
//
// Returns "" if no matching lines are found in any hunk.
func extractPatchContext(patch string, line int, side string, radius int) string {
	if patch == "" || line <= 0 {
		return ""
	}
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(patch))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentNew, currentOld int
	inHunk := false

	withinRange := func(target, candidate int) bool {
		d := target - candidate
		if d < 0 {
			d = -d
		}
		return d <= radius
	}
	emit := func(content string) {
		b.WriteString(content)
		b.WriteByte('\n')
	}

	for scanner.Scan() {
		text := scanner.Text()
		if m := hunkHeaderRe.FindStringSubmatch(text); m != nil {
			currentOld, _ = strconv.Atoi(m[1])
			currentNew, _ = strconv.Atoi(m[2])
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		if text == "" {
			// Blank line within a hunk — treat as context (advances both).
			if (side == "RIGHT" && withinRange(line, currentNew)) ||
				(side == "LEFT" && withinRange(line, currentOld)) {
				emit("")
			}
			currentNew++
			currentOld++
			continue
		}
		switch text[0] {
		case '+':
			if side == "RIGHT" && withinRange(line, currentNew) {
				emit(text[1:])
			}
			currentNew++
		case '-':
			if side == "LEFT" && withinRange(line, currentOld) {
				emit(text[1:])
			}
			currentOld++
		case ' ':
			// Context line — present on both sides.
			if (side == "RIGHT" && withinRange(line, currentNew)) ||
				(side == "LEFT" && withinRange(line, currentOld)) {
				emit(text[1:])
			}
			currentNew++
			currentOld++
		case '\\':
			// "\ No newline at end of file" — skip
		default:
			// Header line that slipped past — exit hunk mode.
			inHunk = false
		}
	}
	return b.String()
}
