package orchestration

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestSummaryEnvelopeCompatibility(t *testing.T) {
	entry := `[{"step_id":"one","summary":"literal ] [ { **界** \\","entities":[]}]`
	batch := []core.StepSummaryInput{{StepID: "one"}}
	want := parseEventSummaries(entry, batch)
	if want.Accepted != 1 {
		t.Fatal("invalid test fixture")
	}
	for _, body := range []string{entry, "Results: " + entry, entry + " Done.", "Results: " + entry + " Done.", "```json\n" + entry + "\n```"} {
		if got := parseEventSummaries(body, batch); !reflect.DeepEqual(got, want) {
			t.Fatalf("envelope changed payload/coverage: %q: %+v", body, got)
		}
	}
	for _, body := range []string{
		entry + " []", entry + " {}", entry + " null", entry + " 42", entry + " true",
		`{"result":` + entry + `}`, `"quoted ` + entry, "Results: " + entry + " [unfinished",
		`{"other":1} Results: ` + entry, "Results: " + entry[:len(entry)-1],
	} {
		if got := parseEventSummaries(body, batch); got.Accepted != 0 || got.CompleteArray {
			t.Fatalf("ambiguous/JSON/truncated envelope accepted: %q: %+v", body, got)
		}
	}
}

func FuzzSummaryEnvelopeFidelity(f *testing.F) {
	f.Add("literal **data** [] {} \\" + "界")
	f.Add("line\nsecond\t\"quoted\"")
	f.Fuzz(func(t *testing.T, summary string) {
		body, err := json.Marshal([]map[string]interface{}{{"step_id": "one", "summary": summary, "entities": []core.EntityRef{{Type: "record", ID: summary}}}})
		if err != nil {
			t.Fatal(err)
		}
		batch := []core.StepSummaryInput{{StepID: "one"}}
		want := parseEventSummaries(string(body), batch)
		for _, wrapped := range []string{"Results: " + string(body), string(body) + " Done.", "Results: " + string(body) + " Done."} {
			if got := parseEventSummaries(wrapped, batch); !reflect.DeepEqual(got, want) {
				t.Fatalf("wrapper changed payload: %+v vs %+v", got, want)
			}
		}
	})
}
