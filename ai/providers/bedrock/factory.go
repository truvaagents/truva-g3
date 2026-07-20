//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"errors"
	"fmt"
	"os"

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

func (f *Factory) Name() string { return "bedrock" }

func (f *Factory) Description() string {
	return "AWS Bedrock unified access to Claude, Llama, Titan and other models"
}

func (f *Factory) Priority() int { return 200 }

// Create preserves legacy registration behavior by returning a client that
// reports AWS configuration failure on first use.
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
	client, err := f.createClient(config)
	if err != nil {
		return &errorClient{err: err}
	}
	return client
}

// CreateValidated constructs Bedrock with error-capable validation.
func (f *Factory) CreateValidated(config *ai.AIConfig) (core.AIClient, error) {
	return f.createClient(config)
}

// CreateRequestClient configures logical request policy for the SDK-native
// Converse surface. HTTP transport integrations are deliberately rejected;
// AWS routing and credentials remain owned by aws.Config.
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
	if integration.EndpointResolver != nil {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Factory", Feature: "endpoint_resolver"}
	}
	if integration.HTTPClient != nil {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Factory", Feature: "http_client"}
	}
	client, err := f.createClient(config)
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
	return client, nil
}

func (f *Factory) createClient(config *ai.AIConfig) (*Client, error) {
	if config == nil {
		return nil, errors.New("bedrock AI config is nil")
	}
	region, err := bedrockRegion(config.Extra)
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
		"model":     config.Model,
	})

	client := NewClient(awsConfig, region, logger)
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
