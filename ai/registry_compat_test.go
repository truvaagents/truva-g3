package ai

import "github.com/truvaagents/truva-g3/core"

func detectBestProvider(logger core.Logger) (string, error) {
	providerName, _, err := detectBestProviderWithAlias(logger)
	return providerName, err
}
