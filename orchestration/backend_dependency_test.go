package orchestration

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBackendPackageDependencyDirection(t *testing.T) {
	rootDependencies := goListDependencies(t, ".")
	if strings.Contains(rootDependencies, "github.com/truvaagents/truva-g3/orchestration/redisprovider\n") {
		t.Fatal("root orchestration package must not import redisprovider")
	}

	conformanceDependencies := goListDependencies(t, "./backendconformance")
	for _, forbidden := range []string{
		"github.com/truvaagents/truva-g3/orchestration/redisprovider\n",
		"github.com/alicebob/miniredis/v2\n",
	} {
		if strings.Contains(conformanceDependencies, forbidden) {
			t.Fatalf("backendconformance dependency closure contains provider/test dependency %q", strings.TrimSpace(forbidden))
		}
	}
}

func goListDependencies(t *testing.T, packagePattern string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", packagePattern)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v: %s", packagePattern, err, output)
	}
	return string(output)
}
