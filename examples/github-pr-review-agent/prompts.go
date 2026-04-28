package main

import (
	"fmt"
	"strings"
)

// BuildShardReviewPrompt assembles the shard review prompt from exact code,
// PR metadata, and review policy. The prompt instructs the model to return
// JSON matching ReviewFinding[].
func BuildShardReviewPrompt(bundle PRBundleManifest, shard ReviewShard, exactContext string) string {
	var b strings.Builder

	b.WriteString("You are reviewing a GitHub pull request shard.\n")
	b.WriteString("Focus on correctness, security, data loss, concurrency, migrations,\n")
	b.WriteString("API compatibility, and test breakage. Ignore style and formatting issues.\n\n")

	b.WriteString("Return ONLY a JSON array matching ReviewFinding[]. No prose.\n")
	b.WriteString("Schema per element: {\"severity\":\"blocking|warning|info\",")
	b.WriteString("\"path\":\"<file>\",\"line\":<int>,\"side\":\"LEFT|RIGHT\",")
	b.WriteString("\"claim\":\"<short>\",\"evidence\":\"<quote nearby code>\",")
	b.WriteString("\"suggestion\":\"<fix>\",\"confidence\":<0.0-1.0>}.\n")
	b.WriteString("Do not invent file paths or line numbers. If evidence is uncertain,\n")
	b.WriteString("lower confidence or omit the finding. An empty array is a valid response.\n\n")

	b.WriteString("PR metadata:\n")
	fmt.Fprintf(&b, "  Title: %s\n", bundle.Title)
	fmt.Fprintf(&b, "  Author: %s\n", bundle.Author)
	fmt.Fprintf(&b, "  Repo: %s/%s #%d\n", bundle.Owner, bundle.Repo, bundle.PullNumber)
	fmt.Fprintf(&b, "  Head SHA: %s\n\n", bundle.HeadSHA)

	if shard.Description != "" {
		fmt.Fprintf(&b, "Shard: %s\n\n", shard.Description)
	}

	b.WriteString("Files in this shard:\n")
	for _, f := range shard.Files {
		fmt.Fprintf(&b, "  - %s (%s, +%d -%d)\n", f.Path, f.Status, f.Additions, f.Deletions)
	}
	b.WriteString("\nExact changed code and surrounding context:\n")
	b.WriteString("---\n")
	b.WriteString(exactContext)
	b.WriteString("\n---\n")

	shardPaths := map[string]struct{}{}
	for _, f := range shard.Files {
		shardPaths[f.Path] = struct{}{}
	}
	var relevant []ExistingComment
	for _, c := range bundle.Comments {
		if _, ok := shardPaths[c.Path]; ok {
			relevant = append(relevant, c)
		}
	}
	if len(relevant) > 0 {
		b.WriteString("\nExisting comments on these files (avoid duplicating):\n")
		for _, c := range relevant {
			fmt.Fprintf(&b, "  - %s:%d %s\n", c.Path, c.Line, truncateString(c.Body, 200))
		}
	}

	return b.String()
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
