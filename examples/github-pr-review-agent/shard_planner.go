package main

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"
)

// EstimatePatchTokens uses a conservative chars/3.5 heuristic for routing only.
// This intentionally over-estimates relative to most BPE tokenizers, which is
// the safe direction for budget gating. Authoritative token counts come from
// the provider response after the call.
const tokenCharRatio = 3.5

func EstimatePatchTokens(file ChangedFileEntry) int {
	// Without the raw patch text on hand (it lives in the artifact store),
	// estimate from additions+deletions assuming ~80 chars per changed line.
	chars := (file.Additions + file.Deletions) * 80
	return int(float64(chars) / tokenCharRatio)
}

// EstimateBundleTokens sums the per-file estimates.
func EstimateBundleTokens(files []ChangedFileEntry) int {
	total := 0
	for _, f := range files {
		total += EstimatePatchTokens(f)
	}
	return total
}

type FileGroup struct {
	Key   string
	Files []ChangedFileEntry
}

// FilterFiles drops files that should not be reviewed deeply per config.
// Returns the kept files and a parallel list of skipped files with reasons.
func FilterFiles(files []ChangedFileEntry, cfg *ReviewConfig) ([]ChangedFileEntry, []SkippedFile) {
	var kept []ChangedFileEntry
	var skipped []SkippedFile
	for _, f := range files {
		if cfg.SkipGenerated && f.IsGenerated {
			skipped = append(skipped, SkippedFile{Path: f.Path, Reason: "generated"})
			continue
		}
		if cfg.SkipLockfiles && f.IsLockfile {
			skipped = append(skipped, SkippedFile{Path: f.Path, Reason: "lockfile"})
			continue
		}
		kept = append(kept, f)
	}
	return kept, skipped
}

// GroupFilesBySemanticBoundary groups files by directory as a proxy for
// package/module. Migration and test files stay with their directory's
// production code rather than getting split into a separate group.
func GroupFilesBySemanticBoundary(files []ChangedFileEntry) []FileGroup {
	groups := map[string]*FileGroup{}
	for _, f := range files {
		key := path.Dir(f.Path)
		g, ok := groups[key]
		if !ok {
			g = &FileGroup{Key: key}
			groups[key] = g
		}
		g.Files = append(g.Files, f)
	}
	out := make([]FileGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// PlanShards packs grouped files into token-bounded shards.
func (a *PRReviewAgent) PlanShards(ctx context.Context, bundle PRBundleManifest) ([]ReviewShard, []SkippedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	files, skipped := FilterFiles(bundle.ChangedFiles, a.Config)
	groups := GroupFilesBySemanticBoundary(files)

	var shards []ReviewShard
	for _, group := range groups {
		current := ReviewShard{Description: group.Key}
		for _, f := range group.Files {
			est := EstimatePatchTokens(f)
			if current.TokenEstimate+est > a.Config.MaxShardTokens && len(current.Files) > 0 {
				shards = append(shards, current)
				current = ReviewShard{Description: group.Key}
			}
			current.Files = append(current.Files, f)
			current.TokenEstimate += est
		}
		if len(current.Files) > 0 {
			shards = append(shards, current)
		}
	}
	return PrioritizeShards(shards), skipped, nil
}

// PrioritizeShards orders shards so the riskiest ones run first. Risk is
// scored by (a) count of risk_hints across files and (b) token estimate.
func PrioritizeShards(shards []ReviewShard) []ReviewShard {
	sort.SliceStable(shards, func(i, j int) bool {
		ri, rj := shardRiskScore(shards[i]), shardRiskScore(shards[j])
		if ri != rj {
			return ri > rj
		}
		return shards[i].TokenEstimate > shards[j].TokenEstimate
	})
	return shards
}

func shardRiskScore(s ReviewShard) int {
	score := 0
	for _, f := range s.Files {
		score += len(f.RiskHints)
	}
	return score
}

// MergeAndDedupeFindings collapses findings that refer to the same issue.
// Dedup key is (path, line, normalized claim). Highest-confidence finding
// wins on collision.
func MergeAndDedupeFindings(in []ReviewFinding) []ReviewFinding {
	seen := map[string]ReviewFinding{}
	for _, f := range in {
		key := f.Path + ":" + strconv.Itoa(f.Line) + ":" + normalizeClaim(f.Claim)
		existing, ok := seen[key]
		if !ok || f.Confidence > existing.Confidence {
			seen[key] = f
		}
	}
	out := make([]ReviewFinding, 0, len(seen))
	for _, f := range seen {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func normalizeClaim(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
