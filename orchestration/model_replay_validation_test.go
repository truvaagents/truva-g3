package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// Optional operator evidence validation. Never calls a provider and does not
// import the AI module. The default test suite needs no retained execution data.
func TestCapturedModelReplayProductionParsing(t *testing.T) {
	inputPath, resultPath := os.Getenv("TRUVAG3_M1_INPUT"), os.Getenv("TRUVAG3_M1_RESULTS")
	if inputPath == "" || resultPath == "" {
		t.Skip("optional captured replay evidence not provided")
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Interactions []struct {
			Type   string `json:"type"`
			Prompt string `json:"prompt"`
		} `json:"interactions"`
	}
	if err = json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	for _, kind := range []string{"skill_activation_selection", "skill_resource_selection", "activity_compaction", "event_summarization"} {
		var found []string
		for _, s := range input.Interactions {
			if s.Type == kind {
				found = append(found, s.Prompt)
			}
		}
		if len(found) == 0 {
			t.Fatalf("missing category %s", kind)
		}
		prompts = append(prompts, found[0], found[len(found)-1])
	}
	f, err := os.Open(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	for index, prompt := range prompts {
		var row struct {
			Sample int            `json:"sample"`
			Type   string         `json:"type"`
			Result *core.AIResult `json:"result"`
			Error  string         `json:"error"`
		}
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		if row.Sample != index+1 || row.Error != "" || row.Result == nil || row.Result.Response == nil {
			t.Fatalf("invalid evidence sample %d: %s", index+1, row.Error)
		}
		content := row.Result.Response.Content
		switch row.Type {
		case "skill_activation_selection":
			var input struct {
				Candidates []SkillCatalogSummary `json:"skill_candidates"`
			}
			if err := json.Unmarshal([]byte(replayXMLValue(t, prompt, "selector_input")), &input); err != nil {
				t.Fatal(err)
			}
			decision, err := parseSkillActivationSelection(content, input.Candidates)
			if err != nil {
				t.Fatalf("sample %d activation: %v", row.Sample, err)
			}
			t.Logf("sample=%d selected_skills=%d", row.Sample, len(decision.Activate))
		case "skill_resource_selection":
			var input struct {
				Resources []SkillResourceCatalogEntry `json:"resource_candidates"`
			}
			if err := json.Unmarshal([]byte(replayXMLValue(t, prompt, "resource_selector_input")), &input); err != nil {
				t.Fatal(err)
			}
			decision, err := parseSkillResourceSelection(content, input.Resources)
			if err != nil {
				t.Fatalf("sample %d resources: %v", row.Sample, err)
			}
			t.Logf("sample=%d selected_resources=%d", row.Sample, len(decision.Select))
		case "activity_compaction":
			compactor := &LLMActivityCompactor{}
			if _, err := compactor.usableDigest(context.Background(), "offline-replay-validation", content); err != nil {
				t.Fatal(err)
			}
			t.Logf("sample=%d usable_digest_bytes=%d (semantic attribution requires review)", row.Sample, len(content))
		case "event_summarization":
			var batch []core.StepSummaryInput
			for _, match := range regexp.MustCompile(`<step id="([^"]+)">`).FindAllStringSubmatch(prompt, -1) {
				batch = append(batch, core.StepSummaryInput{StepID: match[1]})
			}
			if len(batch) == 0 {
				t.Fatal("missing source step identities")
			}
			outcome := parseEventSummaries(content, batch)
			t.Logf("sample=%d coverage=%s accepted=%d expected=%d rejected=%d", row.Sample, outcome.Outcome, outcome.Accepted, outcome.Expected, outcome.Rejected)
			if outcome.Outcome != "complete" {
				t.Fatalf("incomplete replay coverage: %+v", outcome)
			}
		default:
			t.Fatalf("unexpected replay type %s", row.Type)
		}
	}
}

func replayXMLValue(t *testing.T, prompt, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(prompt, "<"+name+">\n")
	if !ok {
		t.Fatal("missing input wrapper")
	}
	value, _, ok := strings.Cut(rest, "\n</"+name+">")
	if !ok {
		t.Fatal("missing closing input wrapper")
	}
	return value
}
