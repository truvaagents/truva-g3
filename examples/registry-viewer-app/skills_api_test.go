package main

import (
	"strings"
	"testing"
)

func TestSkillAdminLimitsFromEnvironmentAppliesOverrides(t *testing.T) {
	t.Setenv("TRUVAG3_SKILL_AUTHORING_MAX_PACKAGE_BYTES", "2097152")
	t.Setenv("TRUVAG3_SKILL_ADMIN_MAX_DELETE_VERSIONS", "25")
	t.Setenv("TRUVAG3_SKILL_AUTHORING_ADVICE_MAX_OUTPUT_TOKENS", "768")

	authoring, administration, err := skillAdminLimitsFromEnvironment()
	if err != nil {
		t.Fatalf("skillAdminLimitsFromEnvironment() error = %v", err)
	}
	if authoring.MaxPackageBytes != 2097152 {
		t.Fatalf("MaxPackageBytes = %d, want 2097152", authoring.MaxPackageBytes)
	}
	if administration.MaxDeleteVersions != 25 {
		t.Fatalf("MaxDeleteVersions = %d, want 25", administration.MaxDeleteVersions)
	}
	if administration.MaxAuthoringAdviceOutputTokens != 768 {
		t.Fatalf("MaxAuthoringAdviceOutputTokens = %d, want 768", administration.MaxAuthoringAdviceOutputTokens)
	}
}

func TestSkillAdminLimitsFromEnvironmentRejectsNonPositiveOverride(t *testing.T) {
	t.Setenv("TRUVAG3_SKILL_AUTHORING_MAX_RESOURCES", "0")

	_, _, err := skillAdminLimitsFromEnvironment()
	if err == nil {
		t.Fatal("skillAdminLimitsFromEnvironment() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "TRUVAG3_SKILL_AUTHORING_MAX_RESOURCES") {
		t.Fatalf("error = %q, want bounded variable name", err)
	}
}
