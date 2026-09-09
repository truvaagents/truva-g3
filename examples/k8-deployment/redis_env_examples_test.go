package k8deployment

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRunnableExampleRedisEnvironmentTemplates(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "*", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join("..", ".env.example"))
	sort.Strings(files)

	required := []string{
		"# REDIS_URL=redis://localhost:6379/0",
		"# Cluster alternative. Never set this together with REDIS_URL.",
		"# TRUVAG3_REDIS_MODE=cluster",
		"# TRUVAG3_REDIS_ADDRS=localhost:7000,localhost:7001,localhost:7002",
		"# TRUVAG3_REDIS_DB=0",
		"# TRUVAG3_REDIS_NAMESPACE=default",
	}
	connectionVariables := map[string]struct{}{
		"REDIS_URL":               {},
		"TRUVAG3_REDIS_MODE":      {},
		"TRUVAG3_REDIS_ADDRS":     {},
		"TRUVAG3_REDIS_DB":        {},
		"TRUVAG3_REDIS_NAMESPACE": {},
	}

	for _, file := range files {
		contents, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Errorf("read %s: %v", file, readErr)
			continue
		}
		template := string(contents)
		for _, value := range required {
			if !strings.Contains(template, value) {
				t.Errorf("%s is missing Redis example value %q", file, value)
			}
		}

		scanner := bufio.NewScanner(strings.NewReader(template))
		for lineNumber := 1; scanner.Scan(); lineNumber++ {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name, _, found := strings.Cut(line, "=")
			if _, connectionVariable := connectionVariables[name]; found && connectionVariable {
				t.Errorf("%s:%d activates %s; Redis topology examples must remain commented overrides", file, lineNumber, name)
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			t.Errorf("scan %s: %v", file, scanErr)
		}
	}
}
