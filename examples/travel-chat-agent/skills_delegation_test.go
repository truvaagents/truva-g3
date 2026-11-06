package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/redisprovider"
)

func TestTravelSkillConfigDefaults(t *testing.T) {
	config := travelSkillConfig()
	want := []orchestration.SkillBinding{
		{Namespace: "travel", Name: "action-verification", Version: "published", Activation: orchestration.SkillActivationAlways},
		{Namespace: "travel", Name: "devops-delegation", Version: "published", Activation: orchestration.SkillActivationAlways},
		{Namespace: "travel", Name: "travel-search-preparation", Version: "published", Activation: orchestration.SkillActivationAuto},
		{Namespace: "travel", Name: "currency-conversion", Version: "published", Activation: orchestration.SkillActivationAuto},
		{Namespace: "travel", Name: "travel-readiness-assessment", Version: "published", Activation: orchestration.SkillActivationAuto},
		{Namespace: "travel", Name: "weather-assessment", Version: "published", Activation: orchestration.SkillActivationAuto},
	}
	if !config.Enabled || !reflect.DeepEqual(config.Bindings, want) {
		t.Fatalf("Travel defaults = %#v, want enabled with %#v", config, want)
	}
	// Each caller owns its default list; changing one cannot alter later agents.
	config.Bindings[0].Required = true
	if travelSkillConfig().Bindings[0].Required {
		t.Fatal("Travel default bindings share mutable storage")
	}
}

func TestTravelSkillEnvironmentOverrides(t *testing.T) {
	replacement := []orchestration.SkillBinding{{
		Namespace: "travel", Name: "weather-assessment", Version: "published",
		Activation: orchestration.SkillActivationAuto,
	}}
	store := newTravelSkillTestStore(t)
	for _, test := range []struct {
		name    string
		env     map[string]string
		enabled bool
		want    []orchestration.SkillBinding
		wantErr bool
	}{
		{name: "defaults", enabled: true, want: travelSkillConfig().Bindings},
		{name: "disabled without registry", env: map[string]string{"TRUVAG3_SKILLS_ENABLED": "false"}, want: travelSkillConfig().Bindings},
		{name: "replacement", env: map[string]string{"TRUVAG3_SKILL_BINDINGS_JSON": `[{"namespace":"travel","name":"weather-assessment","version":"published","activation":"auto"}]`}, enabled: true, want: replacement},
		{name: "empty replacement", env: map[string]string{"TRUVAG3_SKILL_BINDINGS_JSON": `[]`}, enabled: true, want: []orchestration.SkillBinding{}},
		{name: "invalid replacement", env: map[string]string{"TRUVAG3_SKILL_BINDINGS_JSON": `not-json`}, enabled: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := []orchestration.OrchestratorOption{orchestration.WithSkills(travelSkillConfig())}
			if test.enabled {
				options = append(options, orchestration.WithSkillRegistry(store))
			}
			resolved, err := orchestration.ResolveOrchestratorConfig(orchestration.ConfigResolution{
				Environment: orchestration.EnvironmentCompatible,
				LookupEnv: func(key string) (string, bool) {
					value, present := test.env[key]
					return value, present
				},
				Options: options,
			})
			if test.wantErr {
				if err == nil {
					t.Fatal("invalid environment binding list silently fell back to defaults")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Config.Skills.Enabled != test.enabled || !reflect.DeepEqual(resolved.Config.Skills.Bindings, test.want) {
				t.Fatalf("resolved skills = %#v, want enabled=%v bindings=%#v", resolved.Config.Skills, test.enabled, test.want)
			}
		})
	}
}

func TestTravelSkillPackagesPassStrictValidation(t *testing.T) {
	validator, err := orchestration.NewDefaultSkillPackageValidator(orchestration.DefaultSkillAuthoringLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range travelSkillConfig().Bindings {
		t.Run(binding.Name, func(t *testing.T) {
			input := readTravelSkillPackage(t, filepath.Join("skills", "packages", binding.Namespace, binding.Name+".json"))
			_, result, err := validator.Validate(t.Context(), orchestration.SkillRef{Namespace: binding.Namespace, Name: binding.Name}, input)
			if err != nil || !result.Valid {
				t.Fatalf("package validation = %#v, error = %v", result, err)
			}
		})
	}
}

// These checks describe authored guidance, not proof that a model follows it.
func TestDevOpsDelegationGuidanceContract(t *testing.T) {
	input := readTravelSkillPackage(t, filepath.Join("skills", "packages", "travel", "devops-delegation.json"))
	planning := strings.Join(input.PlanningInstructions, "\n")
	for _, guidance := range []string{
		"complete natural-language request", "currently discovered devops-chat-agent", "devops_operations",
		"current discovered capability and input schema as authoritative", "required string query", "optional string session_id",
		"omit session_id unless a real DevOps session", "ordinary travel work", "absent from current discovery",
		"work remains incomplete",
	} {
		if !strings.Contains(planning, guidance) {
			t.Errorf("planning guidance omits %q", guidance)
		}
	}
	response := strings.Join(input.ResponseInstructions, "\n")
	for _, guidance := range []string{"For a request delegated to DevOps", "approval or missing information", "failure", "limitation", "unverified"} {
		if !strings.Contains(response, guidance) {
			t.Errorf("response guidance omits %q", guidance)
		}
	}
}

func TestTravelSkillSetupSyncAndCheck(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "cksum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("strict skill setup verification requires %s: %v", tool, err)
		}
	}
	store := newTravelSkillTestStore(t)
	handler, err := orchestration.NewSkillAdminHandler(orchestration.SkillAdminHandlerDependencies{
		Registry: store, RevisionReader: store, Administration: store, Audit: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// Run the real wrapper with only public source copied into an isolated layout.
	// No .env/.env.example, cluster configuration, or credentials are copied.
	examplesDir := t.TempDir()
	agentDir := filepath.Join(examplesDir, "travel-chat-agent")
	copyTravelSkillTestFile(t, "setup.sh", filepath.Join(agentDir, "setup.sh"))
	copyTravelSkillTestFile(t, filepath.Join("..", "k8-deployment", "setup-env-lib.sh"), filepath.Join(examplesDir, "k8-deployment", "setup-env-lib.sh"))
	if err := os.CopyFS(filepath.Join(agentDir, "skills", "packages"), os.DirFS(filepath.Join("skills", "packages"))); err != nil {
		t.Fatal(err)
	}
	run := func(verb string, wantSuccess bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "bash", filepath.Join(agentDir, "setup.sh"), verb)
		command.Env = []string{
			"PATH=" + os.Getenv("PATH"), "TMPDIR=" + examplesDir,
			"TRUVAG3_SKILLS_API_URL=" + server.URL + "/api/v1/skills",
			"TRUVAG3_SKIP_SKILLS_SYNC=true", // Explicit maintenance remains strict.
		}
		output, err := command.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("./setup.sh %s: error=%v, want success=%v\n%s", verb, err, wantSuccess, output)
		}
	}
	run("skills-sync", true)
	run("skills-check", true)
	run("skills-sync", true)
	for _, binding := range travelSkillConfig().Bindings {
		published, err := store.GetPublished(t.Context(), orchestration.SkillRef{Namespace: binding.Namespace, Name: binding.Name})
		if err != nil || published.Revision.Ref.Version != 1 {
			t.Fatalf("initial/idempotent publication %s = %#v, %v", binding.Name, published.Revision, err)
		}
	}
	packagePath := filepath.Join(agentDir, "skills", "packages", "travel", "devops-delegation.json")
	input := readTravelSkillPackage(t, packagePath)
	input.ResponseInstructions = append(input.ResponseInstructions, "For a delegated request, distinguish completed findings from pending work.")
	input.ChangeReason = "Exercise isolated package drift and roll-forward"
	content, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packagePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	run("skills-check", false)
	run("skills-sync", true)
	run("skills-check", true)
	published, err := store.GetPublished(t.Context(), orchestration.SkillRef{Namespace: "travel", Name: "devops-delegation"})
	if err != nil || published.Revision.Ref.Version != 2 {
		t.Fatalf("changed publication = %#v, %v", published.Revision, err)
	}
}

func newTravelSkillTestStore(t *testing.T) *redisprovider.SkillStore {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), DB: 0})
	t.Cleanup(func() { _ = client.Close() })
	store, err := redisprovider.NewSkillStore(client)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func readTravelSkillPackage(t *testing.T, path string) orchestration.SkillPackageInput {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input, err := orchestration.DecodeSkillPackageInputJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func copyTravelSkillTestFile(t *testing.T, source, destination string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
