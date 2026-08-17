package k8deployment_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type skillSetupMock struct {
	mu            sync.Mutex
	version       int
	packageBody   json.RawMessage
	putCount      int
	idempotencies map[string]struct{}
}

func (mock *skillSetupMock) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	const skillPath = "/api/v1/skills/test/demo"
	writer.Header().Set("Content-Type", "application/json")
	if request.URL.Path == "/api/v1/skills/schema" && request.Method == http.MethodGet {
		_, _ = writer.Write([]byte(`{"schema":"ready"}`))
		return
	}
	if request.URL.Path == skillPath+"/validate" && request.Method == http.MethodPost {
		var authored json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&authored); err != nil {
			http.Error(writer, `{"code":"invalid"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"normalized": authored,
			"validation": map[string]any{"valid": true, "errors": []any{}, "warnings": []any{}},
		})
		return
	}
	if request.URL.Path != skillPath {
		http.NotFound(writer, request)
		return
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	switch request.Method {
	case http.MethodGet:
		if mock.version == 0 {
			http.Error(writer, `{"code":"skill_not_found"}`, http.StatusNotFound)
			return
		}
		mock.writePublished(writer)
	case http.MethodPut:
		mock.putPublished(writer, request)
	default:
		http.Error(writer, `{"code":"method_not_allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (mock *skillSetupMock) putPublished(writer http.ResponseWriter, request *http.Request) {
	expected := "*"
	if mock.version > 0 {
		expected = fmt.Sprintf(`"token-%d"`, mock.version)
	}
	if mock.version == 0 && request.Header.Get("If-None-Match") != expected ||
		mock.version > 0 && request.Header.Get("If-Match") != expected {
		http.Error(writer, `{"code":"precondition_failed"}`, http.StatusPreconditionFailed)
		return
	}
	idempotency := request.Header.Get("Idempotency-Key")
	if idempotency == "" {
		http.Error(writer, `{"code":"missing_idempotency"}`, http.StatusBadRequest)
		return
	}
	if _, found := mock.idempotencies[idempotency]; found {
		http.Error(writer, `{"code":"duplicate_test_key"}`, http.StatusConflict)
		return
	}
	var authored json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&authored); err != nil {
		http.Error(writer, `{"code":"invalid"}`, http.StatusBadRequest)
		return
	}
	mock.idempotencies[idempotency] = struct{}{}
	mock.packageBody = authored
	mock.version++
	mock.putCount++
	status, outcome := http.StatusOK, "updated"
	if mock.version == 1 {
		status, outcome = http.StatusCreated, "created"
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"result": map[string]any{"outcome": outcome},
	})
}

func (mock *skillSetupMock) writePublished(writer http.ResponseWriter) {
	manifestHash := "sha256:" + strings.Repeat("a", 64)
	ref := map[string]any{
		"ref":     map[string]any{"namespace": "test", "name": "demo"},
		"version": mock.version, "manifest_hash": manifestHash,
	}
	writer.Header().Set("ETag", fmt.Sprintf(`"token-%d"`, mock.version))
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"revision": map[string]any{
			"ref": ref,
			"metadata": map[string]any{
				"ref":               map[string]any{"namespace": "test", "name": "demo"},
				"published_version": mock.version, "status": "published",
			},
		},
		"package":  mock.packageBody,
		"manifest": map[string]any{"ref": ref},
	})
}

func (mock *skillSetupMock) state() (putCount, version int) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	return mock.putCount, mock.version
}

func TestSkillSetupHelpersReconcileGitPackages(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "cksum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	mock := &skillSetupMock{idempotencies: make(map[string]struct{})}
	server := httptest.NewServer(http.HandlerFunc(mock.serveHTTP))
	defer server.Close()

	tempDir := t.TempDir()
	versionOne := filepath.Join(tempDir, "v1.json")
	versionOneReasonOnly := filepath.Join(tempDir, "v1-reason.json")
	versionTwo := filepath.Join(tempDir, "v2.json")
	writeSkillPackage(t, versionOne, "first behavior", "Initial test")
	writeSkillPackage(t, versionOneReasonOnly, "first behavior", "Reason-only edit")
	writeSkillPackage(t, versionTwo, "second behavior", "Behavior update")

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(root, "examples", "k8-deployment", "setup-env-lib.sh")
	baseURL := server.URL + "/api/v1/skills"

	runSkillHelper(t, library, baseURL, "check", versionOne, false)
	runSkillHelper(t, library, baseURL, "sync", versionOne, true)
	runSkillHelper(t, library, baseURL, "check", versionOne, true)
	runSkillHelper(t, library, baseURL, "sync", versionOne, true)
	runSkillHelper(t, library, baseURL, "sync", versionOneReasonOnly, true)
	putCount, _ := mock.state()
	if putCount != 1 {
		t.Fatalf("equivalent content should not publish again; puts = %d", putCount)
	}

	runSkillHelper(t, library, baseURL, "check", versionTwo, false)
	runSkillHelper(t, library, baseURL, "sync", versionTwo, true)
	runSkillHelper(t, library, baseURL, "check", versionTwo, true)
	putCount, publishedVersion := mock.state()
	if putCount != 2 || publishedVersion != 2 {
		t.Fatalf("changed content should roll forward once; puts=%d version=%d", putCount, publishedVersion)
	}
}

func TestAgentSkillDirectoryHelpersDeriveIdentityAndReconcile(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "cksum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	mock := &skillSetupMock{idempotencies: make(map[string]struct{})}
	server := httptest.NewServer(http.HandlerFunc(mock.serveHTTP))
	defer server.Close()

	tempDir := t.TempDir()
	packagesDir := filepath.Join(tempDir, "skills", "packages")
	packagePath := filepath.Join(packagesDir, "test", "demo.json")
	if err := os.MkdirAll(filepath.Dir(packagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillPackage(t, packagePath, "directory behavior", "Directory test")

	library := skillSetupLibraryPath(t)
	baseURL := server.URL + "/api/v1/skills"
	runAgentSkillHelper(t, library, baseURL, "check", packagesDir, false)
	runAgentSkillHelper(t, library, baseURL, "sync", packagesDir, true)
	runAgentSkillHelper(t, library, baseURL, "check", packagesDir, true)

	putCount, publishedVersion := mock.state()
	if putCount != 1 || publishedVersion != 1 {
		t.Fatalf("directory sync should publish exactly once; puts=%d version=%d", putCount, publishedVersion)
	}
}

func TestAgentSkillDirectoryHelpersRejectAmbiguousLayouts(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "cksum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	tests := []struct {
		name     string
		relative string
	}{
		{name: "missing namespace", relative: "demo.json"},
		{name: "extra path level", relative: "test/drafts/demo.json"},
		{name: "non JSON file", relative: "test/demo.txt"},
		{name: "invalid namespace slug", relative: "Invalid/demo.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packagesDir := filepath.Join(t.TempDir(), "skills", "packages")
			packagePath := filepath.Join(packagesDir, test.relative)
			if err := os.MkdirAll(filepath.Dir(packagePath), 0o755); err != nil {
				t.Fatal(err)
			}
			writeSkillPackage(t, packagePath, "invalid layout", "Test")

			runAgentSkillHelper(
				t,
				skillSetupLibraryPath(t),
				"http://127.0.0.1:1/api/v1/skills",
				"sync",
				packagesDir,
				false,
			)
		})
	}
}

func TestAgentSkillDirectoryHelpersRejectSymbolicLinks(t *testing.T) {
	tempDir := t.TempDir()
	packagesDir := filepath.Join(tempDir, "skills", "packages")
	targetPath := filepath.Join(tempDir, "target.json")
	linkPath := filepath.Join(packagesDir, "test", "demo.json")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillPackage(t, targetPath, "linked behavior", "Test")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	runAgentSkillHelper(
		t,
		skillSetupLibraryPath(t),
		"http://127.0.0.1:1/api/v1/skills",
		"sync",
		packagesDir,
		false,
	)
}

func TestAgentSkillDirectoryHelpersTreatMissingDirectoryAsNoOp(t *testing.T) {
	runAgentSkillHelper(
		t,
		skillSetupLibraryPath(t),
		"http://127.0.0.1:1/api/v1/skills",
		"sync",
		filepath.Join(t.TempDir(), "not-present"),
		true,
	)
}

func TestAgentSkillDirectoryHelpersIgnoreMacOSMetadata(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "cksum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	mock := &skillSetupMock{idempotencies: make(map[string]struct{})}
	server := httptest.NewServer(http.HandlerFunc(mock.serveHTTP))
	defer server.Close()
	packagesDir := writeAgentSkillPackage(t)
	for _, path := range []string{
		filepath.Join(packagesDir, ".DS_Store"),
		filepath.Join(packagesDir, "test", ".DS_Store"),
	} {
		if err := os.WriteFile(path, []byte("finder metadata"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	output, err := runAgentSkillFunction(
		skillSetupLibraryPath(t),
		server.URL+"/api/v1/skills",
		packagesDir,
		"truvag3_sync_agent_skills",
		nil,
	)
	if err != nil {
		t.Fatalf("macOS metadata should not fail skill synchronization: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Ignoring macOS metadata") {
		t.Fatalf("ignored metadata should remain visible in setup output\n%s", output)
	}
	putCount, _ := mock.state()
	if putCount != 1 {
		t.Fatalf("the JSON package should still be synchronized once; puts = %d", putCount)
	}
}

func TestAutomaticAgentSkillPreparationWarnsAndContinues(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "cksum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.NotFound(writer, request)
	}))
	defer server.Close()
	packagesDir := writeAgentSkillPackage(t)
	baseURL := server.URL + "/api/v1/skills"
	library := skillSetupLibraryPath(t)

	strictOutput, strictErr := runAgentSkillFunction(
		library,
		baseURL,
		packagesDir,
		"truvag3_sync_agent_skills",
		[]string{"TRUVAG3_SKIP_SKILLS_SYNC=true"},
	)
	if strictErr == nil {
		t.Fatalf("explicit skill synchronization unexpectedly succeeded\n%s", strictOutput)
	}
	if !strings.Contains(string(strictOutput), "[ERROR]") {
		t.Fatalf("explicit synchronization should report a strict error\n%s", strictOutput)
	}
	mu.Lock()
	strictRequests := requests
	mu.Unlock()
	if strictRequests != 1 {
		t.Fatalf("strict synchronization should fail fast on HTTP 404; requests = %d", strictRequests)
	}

	prepareOutput, prepareErr := runAgentSkillFunction(
		library,
		baseURL,
		packagesDir,
		"truvag3_prepare_agent_skills",
		[]string{"_TRUVAG3_SKILLS_RETRY_DELAY_SECONDS=0"},
	)
	if prepareErr != nil {
		t.Fatalf("automatic skill preparation blocked deployment: %v\n%s", prepareErr, prepareOutput)
	}
	output := string(prepareOutput)
	if !strings.Contains(output, "[WARN]") ||
		!strings.Contains(output, "continuing deployment without updating published skills") {
		t.Fatalf("automatic preparation should explain its best-effort fallback\n%s", output)
	}
	if strings.Contains(output, "[ERROR]") {
		t.Fatalf("best-effort preparation should report failures as warnings\n%s", output)
	}
	mu.Lock()
	totalRequests := requests
	mu.Unlock()
	if totalRequests != 4 {
		t.Fatalf("automatic preparation should make three bounded HTTP 404 attempts; total requests = %d", totalRequests)
	}
}

func TestAutomaticAgentSkillPreparationRetriesTransientIngress404(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "cksum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	mock := &skillSetupMock{idempotencies: make(map[string]struct{})}
	var mu sync.Mutex
	schemaRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/skills/schema" {
			mu.Lock()
			schemaRequests++
			requestNumber := schemaRequests
			mu.Unlock()
			if requestNumber < 3 {
				http.NotFound(writer, request)
				return
			}
		}
		mock.serveHTTP(writer, request)
	}))
	defer server.Close()

	output, err := runAgentSkillFunction(
		skillSetupLibraryPath(t),
		server.URL+"/api/v1/skills",
		writeAgentSkillPackage(t),
		"truvag3_prepare_agent_skills",
		[]string{"_TRUVAG3_SKILLS_RETRY_DELAY_SECONDS=0"},
	)
	if err != nil {
		t.Fatalf("automatic preparation did not absorb transient ingress 404s: %v\n%s", err, output)
	}
	mu.Lock()
	gotSchemaRequests := schemaRequests
	mu.Unlock()
	if gotSchemaRequests != 3 {
		t.Fatalf("schema requests = %d, want 3", gotSchemaRequests)
	}
	putCount, _ := mock.state()
	if putCount != 1 {
		t.Fatalf("skill should be synchronized after ingress convergence; puts = %d", putCount)
	}
}

func TestAgentSkillCheckUsesShortReadinessBudget(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.Error(writer, `{"code":"starting"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	output, err := runAgentSkillFunction(
		skillSetupLibraryPath(t),
		server.URL+"/api/v1/skills",
		writeAgentSkillPackage(t),
		"truvag3_check_agent_skills",
		[]string{"_TRUVAG3_SKILLS_RETRY_DELAY_SECONDS=0"},
	)
	if err == nil {
		t.Fatalf("check unexpectedly succeeded while the API was unavailable\n%s", output)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 3 {
		t.Fatalf("read-only check requests = %d, want 3", requests)
	}
}

func TestEventDrivenInfrastructureDoesNotLeakLocalEndpoints(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	setupPath := filepath.Join(root, "examples", "event-driven-agent", "setup.sh")
	command := exec.Command("bash", "-c", `
set -e
source "$EVENT_AGENT_SETUP" help >/dev/null
truvag3_setup_infra() {
    [ -z "${REDIS_URL+x}" ]
    [ -z "${OTEL_EXPORTER_OTLP_ENDPOINT+x}" ]
}
REDIS_URL=redis://localhost:6379
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
setup_shared_infra
[ "$REDIS_URL" = "redis://localhost:6379" ]
[ "$OTEL_EXPORTER_OTLP_ENDPOINT" = "http://localhost:4318" ]
`)
	command.Env = append(os.Environ(),
		"EVENT_AGENT_SETUP="+setupPath,
		"TRUVAG3_CONTAINER_RUNTIME=docker",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("event-driven infrastructure endpoint isolation failed: %v\n%s", err, output)
	}
}

func TestAutomaticAgentSkillPreparationCanBeSkipped(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.NotFound(writer, request)
	}))
	defer server.Close()

	output, err := runAgentSkillFunction(
		skillSetupLibraryPath(t),
		server.URL+"/api/v1/skills",
		writeAgentSkillPackage(t),
		"truvag3_prepare_agent_skills",
		[]string{"TRUVAG3_SKIP_SKILLS_SYNC=true"},
	)
	if err != nil {
		t.Fatalf("skipped automatic skill preparation failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "TRUVAG3_SKIP_SKILLS_SYNC=true") {
		t.Fatalf("skip should be visible in setup output\n%s", output)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("skip should avoid Skills API calls; requests = %d", requests)
	}
}

func skillSetupLibraryPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "examples", "k8-deployment", "setup-env-lib.sh")
}

func writeSkillPackage(t *testing.T, path, behavior, reason string) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"display_name":          "Demo",
		"description":           "Use when " + behavior + " is needed.",
		"domains":               []string{"test"},
		"planning_instructions": []string{"Apply " + behavior + "."},
		"change_reason":         reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeAgentSkillPackage(t *testing.T) string {
	t.Helper()
	packagesDir := filepath.Join(t.TempDir(), "skills", "packages")
	packagePath := filepath.Join(packagesDir, "test", "demo.json")
	if err := os.MkdirAll(filepath.Dir(packagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillPackage(t, packagePath, "automatic setup", "Test")
	return packagesDir
}

func runSkillHelper(t *testing.T, library, baseURL, operation, packagePath string, wantSuccess bool) {
	t.Helper()
	function := "truvag3_check_skill_package"
	if operation == "sync" {
		function = "truvag3_sync_skill_package"
	}
	command := exec.Command("bash", "-c", `
set -e
source "$SKILL_SETUP_LIBRARY" >/dev/null
truvag3_check_skill_tools
`+function+` "$SKILL_API_BASE" test demo "$SKILL_PACKAGE"
`)
	command.Env = append(os.Environ(),
		"SKILL_SETUP_LIBRARY="+library,
		"SKILL_API_BASE="+baseURL,
		"SKILL_PACKAGE="+packagePath,
	)
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("%s failed: %v\n%s", operation, err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("%s unexpectedly succeeded\n%s", operation, output)
	}
}

func runAgentSkillHelper(t *testing.T, library, baseURL, operation, packagesDir string, wantSuccess bool) {
	t.Helper()
	function := "truvag3_check_agent_skills"
	if operation == "sync" {
		function = "truvag3_sync_agent_skills"
	}
	output, err := runAgentSkillFunction(library, baseURL, packagesDir, function, nil)
	if wantSuccess && err != nil {
		t.Fatalf("agent %s failed: %v\n%s", operation, err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("agent %s unexpectedly succeeded\n%s", operation, output)
	}
}

func runAgentSkillFunction(
	library, baseURL, packagesDir, function string,
	extraEnv []string,
) ([]byte, error) {
	command := exec.Command("bash", "-c", `
set -e
source "$SKILL_SETUP_LIBRARY" >/dev/null
"$SKILL_SETUP_FUNCTION" "$SKILL_PACKAGES_DIR" "$SKILL_API_BASE"
`)
	command.Env = append(os.Environ(),
		"SKILL_SETUP_LIBRARY="+library,
		"SKILL_SETUP_FUNCTION="+function,
		"SKILL_API_BASE="+baseURL,
		"SKILL_PACKAGES_DIR="+packagesDir,
	)
	command.Env = append(command.Env, extraEnv...)
	return command.CombinedOutput()
}
