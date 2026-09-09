package k8deployment

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedisClusterValidationManifestContract(t *testing.T) {
	manifestBytes, err := os.ReadFile("redis-cluster-validation.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	for _, required := range []string{
		"clusterIP: None",
		"publishNotReadyAddresses: true",
		"replicas: 3",
		"podManagementPolicy: Parallel",
		"--cluster-enabled yes",
		"--cluster-announce-ip \"$POD_IP\"",
		"--cluster-replicas 0",
		"__TRUVAG3_REDIS_DISTRIBUTION__",
		"__TRUVAG3_REDIS_IMAGE__",
	} {
		if !strings.Contains(manifest, required) {
			t.Errorf("validation manifest is missing %q", required)
		}
	}
	if strings.Contains(manifest, "kind: PersistentVolumeClaim") {
		t.Error("validation-only cluster must use disposable storage")
	}
}

func TestRedisClusterValidationScriptPinsRequiredMatrix(t *testing.T) {
	scriptBytes, err := os.ReadFile("setup-redis-cluster-validation.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, required := range []string{
		"redis:8.8.0-alpine",
		"valkey/valkey:8.1.9-alpine",
		"valkey/valkey:9.1.1-alpine",
		"cluster_state:ok",
		"cluster_slots_assigned:16384",
		"cluster_slots_ok:16384",
		"primaries\" -ne 3",
		"replicas\" -ne 0",
		"configure-cluster <namespace> <deployments...>",
		"configure-standalone <namespace> <deployments...>",
		"TRUVAG3_REDIS_MODE=\"$redis_mode\"",
		"TRUVAG3_REDIS_NAMESPACE=\"$redis_namespace\"",
		"--dry-run=client -o yaml",
		"printf '%s\\n' \"$deployment_config\" | kubectl apply -f -",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("validation setup script is missing %q", required)
		}
	}
}

func TestRedisClusterValidationConfigureRecordsUnchangedTopology(t *testing.T) {
	for _, behavior := range []string{"changed", "unchanged", "failure"} {
		t.Run(behavior, func(t *testing.T) {
			dir := t.TempDir()
			applied := filepath.Join(dir, "applied.yaml")
			// Model kubectl's empty successful output for an unchanged set env.
			stub := `#!/bin/bash
set -eu
case "$1" in
  cluster-info) exit 0 ;;
  get)
    if [[ "$*" == *"-o yaml"* ]]; then
      printf 'kind: Deployment\nmetadata:\n  name: unchanged\n'
    fi
    ;;
  set)
    case "$TEST_SET_ENV_BEHAVIOR" in
      changed) printf 'kind: Deployment\nmetadata:\n  name: changed\n' ;;
      unchanged) exit 0 ;;
      failure) exit 23 ;;
    esac
    ;;
  apply) cat > "$TEST_APPLIED_MANIFEST" ;;
  rollout) test -s "$TEST_APPLIED_MANIFEST" ;;
  *) exit 24 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "setup-redis-cluster-validation.sh", "configure-standalone", "verification", "test-agent")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_SET_ENV_BEHAVIOR="+behavior, "TEST_APPLIED_MANIFEST="+applied)
			output, err := cmd.CombinedOutput()
			if behavior == "failure" {
				if err == nil {
					t.Fatal("set env failure must stop configuration")
				}
				if _, statErr := os.Stat(applied); !os.IsNotExist(statErr) {
					t.Fatal("must not apply a fallback after a failed set env")
				}
				return
			}
			if err != nil {
				t.Fatalf("configure: %v\n%s", err, output)
			}
			manifest, err := os.ReadFile(applied)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(manifest), "name: "+behavior) {
				t.Fatalf("wrong applied manifest: %s", manifest)
			}
		})
	}
}
