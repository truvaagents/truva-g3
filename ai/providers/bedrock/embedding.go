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
type EmbeddingOption func(*embeddingConfig) error

// WithEmbeddingModel selects the Bedrock model ID used for one embedding call.
func WithEmbeddingModel(model string) EmbeddingOption {
	return func(config *embeddingConfig) error {
		if config == nil {
			return errors.New("bedrock embedding configuration is nil")
		}
		if err := validateModelID(model, "bedrock embedding model"); err != nil {
			return err
		}
		config.model = model
		return nil
	}
}

// WithEmbeddingDimensions selects a Titan Text Embeddings V2 output size.
func WithEmbeddingDimensions(dimensions int) EmbeddingOption {
	return func(config *embeddingConfig) error {
		if config == nil {
			return errors.New("bedrock embedding configuration is nil")
		}
		validated, err := embeddingDimensions(dimensions)
		if err != nil {
			return err
		}
		config.dimensions = validated
		return nil
	}
}

// WithEmbeddingNormalization controls Titan Text Embeddings V2 normalization.
func WithEmbeddingNormalization(normalize bool) EmbeddingOption {
	return func(config *embeddingConfig) error {
		if config == nil {
			return errors.New("bedrock embedding configuration is nil")
		}
		config.normalize = &normalize
		return nil
	}
}

func applyEmbeddingOptions(base embeddingConfig, options []EmbeddingOption) (embeddingConfig, error) {
	configured := base
	for index, option := range options {
		if option == nil {
			return embeddingConfig{}, fmt.Errorf("bedrock embedding option %d is nil", index)
		}
		if err := option(&configured); err != nil {
			return embeddingConfig{}, fmt.Errorf("apply Bedrock embedding option %d: %w", index, err)
		}
	}
	if strings.TrimSpace(configured.model) == "" {
		return embeddingConfig{}, errors.New("bedrock embedding model is empty")
	}
	if err := validateEmbeddingModelControls(configured); err != nil {
		return embeddingConfig{}, err
	}
	return configured, nil
}

func validateEmbeddingModelControls(config embeddingConfig) error {
	if config.model == ModelTitanEmbedV1 &&
		(config.dimensions != 0 || config.normalize != nil) {
		return errors.New("bedrock Titan Text Embeddings V1 does not accept dimensions or normalize")
	}
	return nil
}
