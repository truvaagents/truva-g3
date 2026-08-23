package openai

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/ai/providers/openai/modelcatalog"
)

type togetherContractSnapshot struct {
	CheckedAt             string            `json:"checked_at"`
	PreferredBaseURL      string            `json:"preferred_base_url"`
	PreferredModelsStatus int               `json:"preferred_models_status"`
	LegacyBaseURL         string            `json:"legacy_base_url"`
	LegacyModelsStatus    int               `json:"legacy_models_status"`
	Aliases               map[string]string `json:"aliases"`
	Sources               []string          `json:"sources"`
}

func TestTogetherContractSnapshotMatchesBuiltInDefaults(t *testing.T) {
	snapshot := loadTogetherContractSnapshot(t)
	if _, err := time.Parse(time.DateOnly, snapshot.CheckedAt); err != nil {
		t.Fatalf("checked_at is not YYYY-MM-DD: %v", err)
	}
	if snapshot.PreferredBaseURL != "https://api.together.ai/v1" || snapshot.PreferredModelsStatus != 200 {
		t.Fatalf("preferred endpoint snapshot = %q status %d", snapshot.PreferredBaseURL, snapshot.PreferredModelsStatus)
	}
	if snapshot.LegacyBaseURL != "https://api.together.xyz/v1" || snapshot.LegacyModelsStatus != 200 {
		t.Fatalf("legacy endpoint snapshot = %q status %d", snapshot.LegacyBaseURL, snapshot.LegacyModelsStatus)
	}

	for _, alias := range []string{"default", "smart", "fast", "code"} {
		model := snapshot.Aliases[alias]
		if model == "" {
			t.Errorf("snapshot has no %q alias", alias)
			continue
		}
		if got := modelcatalog.Resolve("openai.together", alias); got != model {
			t.Errorf("Together %s alias = %q, snapshot wants %q", alias, got, model)
		}
	}
	if len(snapshot.Sources) != 3 {
		t.Fatalf("snapshot sources = %#v", snapshot.Sources)
	}
}

func loadTogetherContractSnapshot(t *testing.T) togetherContractSnapshot {
	t.Helper()
	encoded, err := os.ReadFile("testdata/together_contract_snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot togetherContractSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
