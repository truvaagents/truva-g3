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
	clientConfig, err := redisprovider.LoadClientConfigFromEnvironment(
		redisprovider.DefaultClientConfig(), os.LookupEnv,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve skills client configuration: %w", err)
	}
	clients, err := redisprovider.NewOwnedClients(clientConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("create skills clients: %w", err)
	}
	storeOptions := []redisprovider.SkillStoreOption{redisprovider.WithSkillStoreLogger(logger)}
	if strings.TrimSpace(keyNamespace) != "" {
		storeOptions = append(storeOptions, redisprovider.WithSkillStoreKeyPrefix(strings.TrimSpace(keyNamespace)+":skills"))
	}
	store, err := redisprovider.NewSkillStore(
		clients.ClientSet().Resolve(redisprovider.ClientRoleSkills), storeOptions...,
	)
	if err != nil {
		_ = clients.Close()
		return nil, nil, fmt.Errorf("create skills store: %w", err)
	}
	authoringLimits, adminLimits, err := skillAdminLimitsFromEnvironment()
	if err != nil {
		_ = clients.Close()
		return nil, nil, err
	}
	handler, err := orchestration.NewSkillAdminHandler(orchestration.SkillAdminHandlerDependencies{
		Registry: store, RevisionReader: store, Administration: store,
		Deletions: store, Audit: store, AuthoringLimits: authoringLimits,
		AdministrationLimits: adminLimits, Logger: logger, Telemetry: telemetry,
	})
	if err != nil {
		_ = clients.Close()
		return nil, nil, fmt.Errorf("create skills HTTP handler: %w", err)
	}
	return handler, clients, nil
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
