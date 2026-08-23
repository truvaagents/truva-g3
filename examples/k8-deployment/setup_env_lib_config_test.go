package k8deployment_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupEnvLibraryCapturesOpenRouterConfiguration(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}

	tempDir := t.TempDir()
	envFile := filepath.Join(tempDir, ".env")
	if err := os.WriteFile(envFile, []byte(strings.Join([]string{
		"OPENROUTER_BASE_URL=https://openrouter.example/v1",
		"TRUVAG3_OPENROUTER_MODEL_DEFAULT=openrouter/auto",
		"TRUVAG3_AI_SSE_EVENT_MAX_BYTES=2097152",
		"TRUVAG3_AI_RETRY_DELAY=250ms",
		"TRUVAG3_SETUP_AI_PROVIDER=together",
		"OPENROUTER_API_KEY=must-not-enter-configmap",
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("bash", "-c", `
set -e
source "$1"
capture="$2"
env_file="$3"

kubectl() {
    printf '%s\n' "$*" >> "$capture"
    if [[ "$1" == "create" ]]; then
        printf 'apiVersion: v1\nkind: ConfigMap\n'
    else
        cat >/dev/null
    fi
}

export OPENROUTER_API_KEY="test-openrouter-key"
truvag3_create_secret "test-provider-keys" "test-namespace"
truvag3_create_configmap "test-provider-config" "test-namespace" "$env_file"
`, "setup-env-contract", skillSetupLibraryPath(t), filepath.Join(tempDir, "kubectl.log"), envFile)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("exercise setup environment library: %v\n%s", err, output)
	}

	capturedBytes, err := os.ReadFile(filepath.Join(tempDir, "kubectl.log"))
	if err != nil {
		t.Fatal(err)
	}
	captured := string(capturedBytes)
	for _, expected := range []string{
		"--from-literal=OPENROUTER_API_KEY=test-openrouter-key",
		"--from-literal=OPENROUTER_BASE_URL=https://openrouter.example/v1",
		"--from-literal=TRUVAG3_OPENROUTER_MODEL_DEFAULT=openrouter/auto",
		"--from-literal=TRUVAG3_AI_SSE_EVENT_MAX_BYTES=2097152",
		"--from-literal=TRUVAG3_AI_RETRY_DELAY=250ms",
	} {
		if !strings.Contains(captured, expected) {
			t.Errorf("generated Kubernetes resources do not contain %q\n%s", expected, captured)
		}
	}
	if strings.Contains(captured, "OPENROUTER_API_KEY=must-not-enter-configmap") {
		t.Errorf("OpenRouter API key from .env leaked into ConfigMap arguments\n%s", captured)
	}
	if strings.Contains(captured, "TRUVAG3_SETUP_AI_PROVIDER") {
		t.Errorf("setup-only provider selector leaked into runtime ConfigMap arguments\n%s", captured)
	}
}

func TestSetupEnvLibraryCanIsolateOneAIProvider(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}

	tempDir := t.TempDir()
	command := exec.Command("bash", "-c", `
set -e
source "$1"
capture="$2"

kubectl() {
    printf '%s\n' "$*" >> "$capture"
    if [[ "$1" == "create" ]]; then
        printf 'apiVersion: v1\nkind: Secret\n'
    else
        cat >/dev/null
    fi
}

export OPENROUTER_API_KEY="test-openrouter-key"
export TOGETHER_API_KEY="test-together-key"
export TRUVAG3_SETUP_AI_PROVIDER="together"
truvag3_create_secret "test-provider-keys" "test-namespace"
`, "setup-env-provider-isolation", skillSetupLibraryPath(t), filepath.Join(tempDir, "kubectl.log"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("exercise setup provider isolation: %v\n%s", err, output)
	}

	capturedBytes, err := os.ReadFile(filepath.Join(tempDir, "kubectl.log"))
	if err != nil {
		t.Fatal(err)
	}
	captured := string(capturedBytes)
	if !strings.Contains(captured, "--from-literal=TOGETHER_API_KEY=test-together-key") {
		t.Errorf("generated Kubernetes Secret omitted selected Together key\n%s", captured)
	}
	if strings.Contains(captured, "OPENROUTER_API_KEY") {
		t.Errorf("generated Kubernetes Secret included unselected OpenRouter key\n%s", captured)
	}
}
