package framework

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const frameworkModulePrefix = "github.com/truvaagents/truva-g3"

type architectureModule struct {
	name     string
	dir      string
	patterns []string
	allowed  map[string]struct{}
}

type goListPackage struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

var architectureBuildProfiles = []struct {
	name string
	tags string
}{
	{name: "default"},
	{name: "integration", tags: "integration"},
}

func TestFrameworkModuleDependencyDAG(t *testing.T) {
	repositoryRoot := architectureRepositoryRoot(t)
	modules := []architectureModule{
		{
			name:     "root",
			dir:      repositoryRoot,
			patterns: []string{"."},
			allowed:  architectureSet("core"),
		},
		{
			name:     "core",
			dir:      filepath.Join(repositoryRoot, "core"),
			patterns: []string{"./..."},
			allowed:  architectureSet(),
		},
		{
			name:     "telemetry",
			dir:      filepath.Join(repositoryRoot, "telemetry"),
			patterns: []string{"./..."},
			allowed:  architectureSet("core"),
		},
		{
			name:     "ai",
			dir:      filepath.Join(repositoryRoot, "ai"),
			patterns: []string{"./..."},
			allowed:  architectureSet("core", "telemetry"),
		},
		{
			name:     "memory",
			dir:      filepath.Join(repositoryRoot, "memory"),
			patterns: []string{"./..."},
			allowed:  architectureSet("core", "telemetry"),
		},
		{
			name:     "resilience",
			dir:      filepath.Join(repositoryRoot, "resilience"),
			patterns: []string{"./..."},
			allowed:  architectureSet("core", "telemetry"),
		},
		{
			name:     "orchestration",
			dir:      filepath.Join(repositoryRoot, "orchestration"),
			patterns: []string{"./..."},
			allowed:  architectureSet("core", "telemetry"),
		},
	}

	for _, module := range modules {
		module := module
		t.Run(module.name, func(t *testing.T) {
			for _, profile := range architectureBuildProfiles {
				profile := profile
				t.Run(profile.name, func(t *testing.T) {
					packages := architectureGoList(t, module, profile.tags)
					violations := make([]string, 0)
					for _, pkg := range packages {
						imports := append([]string{}, pkg.Imports...)
						imports = append(imports, pkg.TestImports...)
						imports = append(imports, pkg.XTestImports...)
						for _, imported := range imports {
							dependency, ok := architectureFrameworkModule(imported)
							if !ok || dependency == module.name {
								continue
							}
							if _, allowed := module.allowed[dependency]; !allowed {
								violations = append(violations, fmt.Sprintf("%s imports %s (%s)", pkg.ImportPath, imported, dependency))
							}
						}
					}
					sort.Strings(violations)
					if len(violations) != 0 {
						t.Fatalf("framework dependency DAG violations:\n%s", strings.Join(violations, "\n"))
					}
				})
			}
		})
	}
}

func architectureRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test location")
	}
	return filepath.Dir(filename)
}

func architectureSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func architectureGoList(t *testing.T, module architectureModule, tags string) []goListPackage {
	t.Helper()
	arguments := []string{"list", "-json"}
	if tags != "" {
		arguments = append(arguments, "-tags", tags)
	}
	arguments = append(arguments, module.patterns...)
	command := exec.CommandContext(t.Context(), "go", arguments...)
	command.Dir = module.dir
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("capture go list output for %s: %v", module.name, err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start go list for %s: %v", module.name, err)
	}

	decoder := json.NewDecoder(output)
	packages := make([]goListPackage, 0)
	for {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			_ = command.Process.Kill()
			t.Fatalf("decode go list output for %s: %v", module.name, err)
		}
		packages = append(packages, pkg)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("go list for %s: %v: %s", module.name, err, strings.TrimSpace(stderr.String()))
	}
	if len(packages) == 0 {
		t.Fatalf("go list for %s returned no packages", module.name)
	}
	return packages
}

func architectureFrameworkModule(importPath string) (string, bool) {
	if importPath == frameworkModulePrefix {
		return "root", true
	}
	if !strings.HasPrefix(importPath, frameworkModulePrefix+"/") {
		return "", false
	}
	remainder := strings.TrimPrefix(importPath, frameworkModulePrefix+"/")
	name, _, _ := strings.Cut(remainder, "/")
	switch name {
	case "ai", "core", "memory", "orchestration", "resilience", "telemetry":
		return name, true
	default:
		return "root", true
	}
}
