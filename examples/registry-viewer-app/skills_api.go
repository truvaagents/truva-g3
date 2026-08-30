package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/orchestration/redisprovider"
)

func newSkillAdminAPI(
	keyNamespace string,
	logger core.Logger,
	telemetry core.Telemetry,
) (httpHandler orchestrationHTTPHandler, closer io.Closer, err error) {
	defaultOptions := []redisprovider.DefaultBackendsOption{
		redisprovider.WithDefaultBackendRoles(redisprovider.ClientRoleSkills),
	}
	if strings.TrimSpace(keyNamespace) != "" {
		defaultOptions = append(defaultOptions, redisprovider.WithDefaultBackendProviderOptions(
			redisprovider.WithNamespace(strings.TrimSpace(keyNamespace)),
		))
	}
	owned, err := redisprovider.NewDefaultBackends(logger, defaultOptions...)
	if err != nil {
		return nil, nil, fmt.Errorf("create default skills backends: %w", err)
	}
	dependencies, err := owned.Backends().SkillAdministrationDependencies()
	if err != nil {
		_ = owned.Close()
		return nil, nil, fmt.Errorf("wire skills administration backends: %w", err)
	}
	authoringLimits, adminLimits, err := skillAdminLimitsFromEnvironment()
	if err != nil {
		_ = owned.Close()
		return nil, nil, err
	}
	dependencies.AuthoringLimits = authoringLimits
	dependencies.AdministrationLimits = adminLimits
	dependencies.Logger = logger
	dependencies.Telemetry = telemetry
	handler, err := orchestration.NewSkillAdminHandler(dependencies)
	if err != nil {
		_ = owned.Close()
		return nil, nil, fmt.Errorf("create skills HTTP handler: %w", err)
	}
	return handler, owned, nil
}

// orchestrationHTTPHandler is the small host-facing surface needed from the
// framework handler and keeps this example's setup independently testable.
type orchestrationHTTPHandler interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}

func skillAdminLimitsFromEnvironment() (orchestration.SkillAuthoringLimits, orchestration.SkillAdministrationLimits, error) {
	authoring := orchestration.DefaultSkillAuthoringLimits()
	administration := orchestration.DefaultSkillAdministrationLimits()
	values := []struct {
		name   string
		target *int
	}{
		{"TRUVAG3_SKILL_AUTHORING_MAX_NAME_CHARS", &authoring.MaxNameChars},
		{"TRUVAG3_SKILL_AUTHORING_MAX_DESCRIPTION_CHARS", &authoring.MaxDescriptionChars},
		{"TRUVAG3_SKILL_AUTHORING_MAX_MANIFEST_TOKENS", &authoring.MaxManifestTokens},
		{"TRUVAG3_SKILL_AUTHORING_MAX_MANIFEST_BYTES", &authoring.MaxManifestBytes},
		{"TRUVAG3_SKILL_AUTHORING_MAX_RESOURCE_TOKENS", &authoring.MaxResourceTokens},
		{"TRUVAG3_SKILL_AUTHORING_MAX_RESOURCE_BYTES", &authoring.MaxResourceBytes},
		{"TRUVAG3_SKILL_AUTHORING_MAX_RESOURCES", &authoring.MaxResources},
		{"TRUVAG3_SKILL_AUTHORING_MAX_PACKAGE_BYTES", &authoring.MaxPackageBytes},
		{"TRUVAG3_SKILL_ADMIN_MAX_DELETE_VERSIONS", &administration.MaxDeleteVersions},
		{"TRUVAG3_SKILL_AUTHORING_ADVICE_MAX_OUTPUT_TOKENS", &administration.MaxAuthoringAdviceOutputTokens},
	}
	for _, value := range values {
		raw, present := os.LookupEnv(value.name)
		if !present {
			continue
		}
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(raw))
		if parseErr != nil || parsed <= 0 {
			return orchestration.SkillAuthoringLimits{}, orchestration.SkillAdministrationLimits{},
				fmt.Errorf("%s must be a positive integer", value.name)
		}
		*value.target = parsed
	}
	if err := authoring.Validate(); err != nil {
		return orchestration.SkillAuthoringLimits{}, orchestration.SkillAdministrationLimits{}, err
	}
	if err := administration.Validate(); err != nil {
		return orchestration.SkillAuthoringLimits{}, orchestration.SkillAdministrationLimits{}, err
	}
	return authoring, administration, nil
}
