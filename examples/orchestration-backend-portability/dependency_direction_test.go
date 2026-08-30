package main

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestApplicationLogicDoesNotImportProviderImplementations(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"github.com/jackc/pgx",
		"github.com/nats-io/nats.go",
		"github.com/redis/go-redis",
		"/internal/natsadapter",
		"/internal/postgresadapter",
		"/internal/redisadapter",
	}
	for _, filename := range files {
		if filename == "backends.go" || strings.HasSuffix(filename, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", filename, err)
			}
			for _, prefix := range forbidden {
				if strings.Contains(path, prefix) {
					t.Errorf("%s imports provider implementation %q; keep it in backends.go or internal adapters", filename, path)
				}
			}
		}
	}
}
