//go:build bedrock
// +build bedrock

package bedrock

import (
	"errors"
	"fmt"
	"strings"
)

// EmbeddingOption configures one Bedrock Titan-shaped embedding request.
// Options override client defaults without mutating the client.
type EmbeddingOption func(*embeddingOverrides) error

type embeddingOverrides struct {
	model            *string
	dimensions       *int32
	normalize        *bool
	normalizationSet bool
}

// WithEmbeddingModel selects the Bedrock model ID used for one embedding call.
func WithEmbeddingModel(model string) EmbeddingOption {
	return func(overrides *embeddingOverrides) error {
		if overrides == nil {
			return errors.New("bedrock embedding overrides are nil")
		}
		if err := validateModelID(model, "bedrock embedding model"); err != nil {
			return err
		}
		selected := model
		overrides.model = &selected
		return nil
	}
}

// WithEmbeddingDimensions selects a Titan Text Embeddings V2 output size.
func WithEmbeddingDimensions(dimensions int) EmbeddingOption {
	return func(overrides *embeddingOverrides) error {
		if overrides == nil {
			return errors.New("bedrock embedding overrides are nil")
		}
		validated, err := embeddingDimensions(dimensions)
		if err != nil {
			return err
		}
		overrides.dimensions = &validated
		return nil
	}
}

// WithEmbeddingNormalization controls Titan Text Embeddings V2 normalization.
func WithEmbeddingNormalization(normalize bool) EmbeddingOption {
	return func(overrides *embeddingOverrides) error {
		if overrides == nil {
			return errors.New("bedrock embedding overrides are nil")
		}
		overrides.normalize = &normalize
		overrides.normalizationSet = true
		return nil
	}
}

// WithoutEmbeddingNormalization omits the Titan V2 normalize field for one
// call, even when the client has an inherited normalization default.
func WithoutEmbeddingNormalization() EmbeddingOption {
	return func(overrides *embeddingOverrides) error {
		if overrides == nil {
			return errors.New("bedrock embedding overrides are nil")
		}
		overrides.normalize = nil
		overrides.normalizationSet = true
		return nil
	}
}

func applyEmbeddingOptions(base embeddingConfig, options []EmbeddingOption) (embeddingConfig, error) {
	configured := base
	overrides := embeddingOverrides{}
	for index, option := range options {
		if option == nil {
			return embeddingConfig{}, fmt.Errorf("bedrock embedding option %d is nil", index)
		}
		if err := option(&overrides); err != nil {
			return embeddingConfig{}, fmt.Errorf("apply Bedrock embedding option %d: %w", index, err)
		}
	}
	if overrides.model != nil {
		configured.model = *overrides.model
	}
	if overrides.dimensions != nil {
		configured.dimensions = *overrides.dimensions
	}
	if overrides.normalizationSet {
		configured.normalize = overrides.normalize
	}
	if strings.TrimSpace(configured.model) == "" {
		return embeddingConfig{}, errors.New("bedrock embedding model is empty")
	}
	if isTitanEmbedV1Model(configured.model) {
		// A per-call V1 migration pin supersedes inherited V2-only client
		// defaults. Non-zero dimensions or normalization explicitly supplied on
		// this same call remain present so validation rejects the incompatible
		// combination; an explicit zero dimension is still omission.
		if overrides.dimensions == nil {
			configured.dimensions = 0
		}
		if !overrides.normalizationSet {
			configured.normalize = nil
		}
	}
	if err := validateEmbeddingModelControls(configured); err != nil {
		return embeddingConfig{}, err
	}
	return configured, nil
}

func validateEmbeddingModelControls(config embeddingConfig) error {
	if isTitanEmbedV1Model(config.model) &&
		(config.dimensions != 0 || config.normalize != nil) {
		return errors.New("bedrock Titan Text Embeddings V1 does not accept dimensions or normalize")
	}
	return nil
}

func isTitanEmbedV1Model(model string) bool {
	return bedrockModelInFamily(model, ModelTitanEmbedV1)
}

func titanEmbeddingSemanticModel(model string) string {
	if isTitanEmbedV1Model(model) {
		return titanEmbeddingV1SemanticModel
	}
	return titanEmbeddingV2SemanticModel
}
