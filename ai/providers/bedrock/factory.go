//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func init() { ai.MustRegister(&Factory{}) }

// Factory creates AWS Bedrock clients.
type Factory struct{}

var _ ai.ValidatedProviderFactory = (*Factory)(nil)
var _ ai.RequestProviderFactory = (*Factory)(nil)
var _ ai.ProviderRequestTimeoutFactory = (*Factory)(nil)

func (f *Factory) Name() string { return "bedrock" }

func (f *Factory) Description() string {
	return "AWS Bedrock unified access to Claude, Llama, Titan and other models"
}

func (f *Factory) Priority() int { return 200 }

// DefaultRequestTimeout leaves headroom for Bedrock's long-running model
// inference while remaining overrideable through ai.WithTimeout.
func (f *Factory) DefaultRequestTimeout() time.Duration {
	return defaultBedrockRequestTimeout
}

// Create preserves legacy registration behavior by returning a client that
// reports AWS configuration failure on first use.
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
	client, err := f.createClient(config, false)
	if err != nil {
		return &errorClient{err: err}
	}
	return client
}

// CreateValidated constructs Bedrock with error-capable validation.
func (f *Factory) CreateValidated(config *ai.AIConfig) (core.AIClient, error) {
	return f.createClient(config, false)
}

// CreateRequestClient configures logical request policy for the SDK-native
// Converse surface. EndpointResolver may select only the opaque SDK modelId;
// HTTP transport and credentials remain owned by aws.Config and are rejected.
func (f *Factory) CreateRequestClient(
	config *ai.AIConfig,
	integration ai.ProviderIntegrationConfig,
) (core.AIRequestClient, error) {
	if config != nil && len(config.Headers) > 0 {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Factory", Feature: "headers"}
	}
	if integration.CredentialSource != nil {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Factory", Feature: "credential_source"}
	}
	if integration.HTTPClient != nil {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Factory", Feature: "http_client"}
	}
	client, err := f.createClient(config, integration.EndpointResolver != nil)
	if err != nil {
		return nil, err
	}
	engine, err := newRequestPolicyEngineWithIntegration(
		integration.RequestRules,
		integration.RequestMiddleware,
		integration.CompatibilityMode,
	)
	if err != nil {
		return nil, fmt.Errorf("configure Bedrock request policy: %w", err)
	}
	client.requestPolicy = engine
	client.endpointResolver = integration.EndpointResolver
	return client, nil
}

func (f *Factory) createClient(config *ai.AIConfig, hasEndpointResolver bool) (*Client, error) {
	if config == nil {
		return nil, errors.New("bedrock AI config is nil")
	}
	region, err := bedrockRegion(config.Extra)
	if err != nil {
		return nil, err
	}
	if err := validateImplicitBedrockDefault(region, config.Model, hasEndpointResolver); err != nil {
		return nil, err
	}
	embedding, err := bedrockEmbeddingConfig(config.Extra)
	if err != nil {
		return nil, err
	}
	awsConfig, err := loadAWSConfig(context.Background(), region, config.Extra)
	if err != nil {
		return nil, err
	}

	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	} else if componentLogger, ok := logger.(core.ComponentAwareLogger); ok {
		logger = componentLogger.WithComponent("framework/ai")
	}
	logger.Info("Bedrock provider initialized", map[string]interface{}{
		"operation": "ai_provider_init",
		"provider":  "bedrock",
		"region":    region,
	})

	client := NewClient(awsConfig, region, logger)
	client.embedding = embedding
	if config.Telemetry != nil {
		client.SetTelemetry(config.Telemetry)
	}
	if config.Timeout > 0 {
		client.requestTimeout = config.Timeout
	}
	if config.MaxRetries >= 0 {
		client.MaxRetries = config.MaxRetries
	}
	if config.Model != "" {
		client.DefaultModel = config.Model
	}
	if config.Temperature > 0 {
		client.DefaultTemperature = config.Temperature
	}
	if config.MaxTokens > 0 {
		client.DefaultMaxTokens = config.MaxTokens
	}
	return client, nil
}

func validateImplicitBedrockDefault(region, configuredModel string, hasEndpointResolver bool) error {
	if configuredModel != "" || hasEndpointResolver || region == "us-east-1" {
		return nil
	}
	return fmt.Errorf(
		"bedrock implicit default model %q is not available for direct in-region inference in region %q; "+
			"configure ai.WithModel with an AWS-supported model or inference-profile ID, "+
			"or use ai.WithEndpointResolver for an explicit route",
		ModelClaudeSonnet5,
		region,
	)
}

func bedrockEmbeddingConfig(extra map[string]interface{}) (embeddingConfig, error) {
	result := embeddingConfig{model: ModelTitanEmbedV2}
	if value, exists := extra["embedding_model"]; exists {
		model, ok := value.(string)
		if !ok || strings.TrimSpace(model) == "" {
			return embeddingConfig{}, fmt.Errorf("bedrock embedding_model must be a non-empty string, got %T", value)
		}
		if err := validateModelID(model, "bedrock embedding_model"); err != nil {
			return embeddingConfig{}, err
		}
		result.model = model
	}
	if value, exists := extra["embedding_dimensions"]; exists {
		dimensions, err := embeddingDimensions(value)
		if err != nil {
			return embeddingConfig{}, err
		}
		result.dimensions = dimensions
	}
	if value, exists := extra["embedding_normalize"]; exists {
		normalize, ok := value.(bool)
		if !ok {
			return embeddingConfig{}, fmt.Errorf("bedrock embedding_normalize must be a bool, got %T", value)
		}
		result.normalize = &normalize
	}
	if err := validateEmbeddingModelControls(result); err != nil {
		return embeddingConfig{}, err
	}
	return result, nil
}

func embeddingDimensions(value interface{}) (int32, error) {
	var dimensions int64
	switch number := value.(type) {
	case int:
		dimensions = int64(number)
	case int32:
		dimensions = int64(number)
	case int64:
		dimensions = number
	default:
		return 0, fmt.Errorf("bedrock embedding_dimensions must be an integer, got %T", value)
	}
	switch dimensions {
	case 0, 256, 512, 1024:
		return int32(dimensions), nil
	default:
		return 0, errors.New("bedrock embedding_dimensions must be 0, 256, 512, or 1024")
	}
}

func bedrockRegion(extra map[string]interface{}) (string, error) {
	if value, exists := extra["region"]; exists {
		region, ok := value.(string)
		if !ok || region == "" {
			return "", fmt.Errorf("bedrock region must be a non-empty string, got %T", value)
		}
		return region, nil
	}
	if region := os.Getenv("AWS_REGION"); region != "" {
		return region, nil
	}
	if region := os.Getenv("AWS_DEFAULT_REGION"); region != "" {
		return region, nil
	}
	return "us-east-1", nil
}

func loadAWSConfig(ctx context.Context, region string, extra map[string]interface{}) (aws.Config, error) {
	accessKey, hasAccessKey, err := optionalString(extra, "aws_access_key_id")
	if err != nil {
		return aws.Config{}, err
	}
	secretKey, hasSecretKey, err := optionalString(extra, "aws_secret_access_key")
	if err != nil {
		return aws.Config{}, err
	}
	if hasAccessKey != hasSecretKey {
		return aws.Config{}, errors.New("bedrock static credentials require both aws_access_key_id and aws_secret_access_key")
	}
	if !hasAccessKey {
		return CreateAWSConfig(ctx, region)
	}
	sessionToken, _, err := optionalString(extra, "aws_session_token")
	if err != nil {
		return aws.Config{}, err
	}
	provider := credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)
	return CreateAWSConfig(ctx, region, provider)
}

func optionalString(extra map[string]interface{}, key string) (string, bool, error) {
	value, exists := extra[key]
	if !exists || value == nil {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return "", false, fmt.Errorf("bedrock %s must be a non-empty string, got %T", key, value)
	}
	return text, true, nil
}

// DetectEnvironment checks whether AWS Bedrock credentials are discoverable.
func (f *Factory) DetectEnvironment() (priority int, available bool) {
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return f.Priority(), true
	}
	if os.Getenv("AWS_PROFILE") != "" {
		return f.Priority(), true
	}
	if os.Getenv("AWS_EXECUTION_ENV") != "" || os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		return f.Priority() + 50, true
	}
	if os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" {
		return f.Priority() + 50, true
	}
	homeDirectory, err := os.UserHomeDir()
	if err == nil {
		if _, err := os.Stat(homeDirectory + "/.aws/credentials"); err == nil {
			return f.Priority(), true
		}
	}
	return 0, false
}

type errorClient struct{ err error }

func (client *errorClient) GenerateResponse(
	context.Context,
	string,
	*core.AIOptions,
) (*core.AIResponse, error) {
	return nil, client.err
}
