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
