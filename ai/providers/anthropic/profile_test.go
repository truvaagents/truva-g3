package anthropic

import (
	"strings"
	"testing"
)

func TestAnthropicRequestProfileValidationMatrix(t *testing.T) {
	valid := requestProfile{
		fingerprintIdentity: directProfileIdentity,
		semanticModel:       "claude-sonnet-4-5-20250929",
		wireModel:           "claude-sonnet-4-5-20250929",
		modelField:          modelInBody,
		versionPlacement:    versionInHeader,
		version:             APIVersion,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*requestProfile)
	}{
		{name: "empty identity", mutate: func(profile *requestProfile) { profile.fingerprintIdentity = " " }},
		{name: "empty semantic model", mutate: func(profile *requestProfile) { profile.semanticModel = " " }},
		{name: "empty wire model", mutate: func(profile *requestProfile) { profile.wireModel = " " }},
		{name: "invalid model placement", mutate: func(profile *requestProfile) { profile.modelField = modelFieldMode(255) }},
		{name: "invalid version placement", mutate: func(profile *requestProfile) { profile.versionPlacement = versionPlacement(255) }},
		{name: "empty version", mutate: func(profile *requestProfile) { profile.version = " " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := valid
			test.mutate(&profile)
			if err := profile.validate(); err == nil {
				t.Fatalf("invalid profile accepted: %#v", profile)
			}
		})
	}
}

func TestAnthropicRequestProfileSeparatesDirectAndVertexWireIdentity(t *testing.T) {
	client := &Client{}
	directSemantics := &requestSemantics{
		ProviderAlias: "anthropic",
		SemanticModel: "claude-sonnet-4-5-20250929",
	}
	direct, err := client.requestProfile(directSemantics, resolvedRoute{deployment: "route-model-must-not-win"})
	if err != nil {
		t.Fatal(err)
	}
	if direct.wireModel != directSemantics.SemanticModel || direct.modelField != modelInBody ||
		direct.versionPlacement != versionInHeader || direct.version != APIVersion {
		t.Fatalf("direct profile = %#v", direct)
	}

	vertexSemantics := &requestSemantics{
		ProviderAlias: "anthropic.vertex",
		SemanticModel: "claude-sonnet-4-5-20250929",
	}
	if _, err := client.requestProfile(vertexSemantics, resolvedRoute{}); err == nil || !strings.Contains(err.Error(), "publisher model is empty") {
		t.Fatalf("missing Vertex publisher model error = %v", err)
	}
	vertex, err := client.requestProfile(vertexSemantics, resolvedRoute{deployment: "claude-sonnet-4-5@20250929"})
	if err != nil {
		t.Fatal(err)
	}
	if vertex.semanticModel != vertexSemantics.SemanticModel || vertex.wireModel != "claude-sonnet-4-5@20250929" ||
		vertex.modelField != modelInRoute || vertex.versionPlacement != versionInBody ||
		vertex.version != vertexAPIVersion || vertex.fingerprintIdentity != vertexProfileIdentity {
		t.Fatalf("Vertex profile = %#v", vertex)
	}
}
