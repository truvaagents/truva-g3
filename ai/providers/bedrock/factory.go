//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func init() {
	ai.MustRegister(&Factory{})
}

// Factory creates AWS Bedrock AI clients
type Factory struct{}

// Name returns the provider name
func (f *Factory) Name() string {
	return "bedrock"
}

// Description returns provider description
func (f *Factory) Description() string {
	return "AWS Bedrock unified access to Claude, Llama, Titan and other models"
}

// Priority returns provider priority
func (f *Factory) Priority() int {
	return 200 // Lower than cloud providers but higher than local
}

// Create creates a new AWS Bedrock client
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
	ctx := context.Background()

	// Get region from config or environment
	region := config.Extra["region"]
	if region == nil || region == "" {
		region = os.Getenv("AWS_REGION")
		if region == "" {
			region = os.Getenv("AWS_DEFAULT_REGION")
			if region == "" {
				region = "us-east-1" // Default region
			}
		}
	}

	// Create AWS configuration
	var awsCfg aws.Config
	var err error

	// Check for explicit credentials in config
	if config.Extra["aws_access_key_id"] != nil && config.Extra["aws_secret_access_key"] != nil {
		accessKey := config.Extra["aws_access_key_id"].(string)
		secretKey := config.Extra["aws_secret_access_key"].(string)
		sessionToken := ""
		if config.Extra["aws_session_token"] != nil {
			sessionToken = config.Extra["aws_session_token"].(string)
		}

		// Create static credentials provider
		credProvider := credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)
		awsCfg, err = CreateAWSConfig(ctx, region.(string), credProvider)
	} else {
		// Use default credential chain (IAM role, env vars, ~/.aws/credentials, etc.)
		awsCfg, err = CreateAWSConfig(ctx, region.(string))
	}

	if err != nil {
		// Return a client that will error on first use
		// This allows the provider to be registered even if AWS isn't configured
		return &errorClient{err: err}
	}

	// Get logger from config with proper component wrapping
	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		logger = cal.WithComponent("framework/ai")
	}

	// Log provider initialization
	logger.Info("Bedrock provider initialized", map[string]interface{}{
		"operation": "ai_provider_init",
		"provider":  "bedrock",
		"region":    region,
		"model":     config.Model,
	})

	// Create the client
	client := NewClient(awsCfg, region.(string), logger)

	// Set telemetry for distributed tracing
	if config.Telemetry != nil {
		client.SetTelemetry(config.Telemetry)
	}

	// Apply timeout if specified
	if config.Timeout > 0 {
		client.BaseClient.HTTPClient.Timeout = config.Timeout
	}

	// Apply retry configuration. Honors 0 ("no retries") as a valid setting —
	// the AIConfig defaults to a sentinel before NewClient resolves it via
	// option / env var / default precedence, so any non-negative value here
	// means "operator chose this on purpose".
	if config.MaxRetries >= 0 {
		client.BaseClient.MaxRetries = config.MaxRetries
	}

	// Apply model defaults
	if config.Model != "" {
		client.BaseClient.DefaultModel = config.Model
	}

	// Apply temperature default
	if config.Temperature > 0 {
		client.BaseClient.DefaultTemperature = config.Temperature
	}

	// Apply max tokens default
	if config.MaxTokens > 0 {
		client.BaseClient.DefaultMaxTokens = config.MaxTokens
	}

	return client
}

// DetectEnvironment checks if AWS Bedrock is configured
func (f *Factory) DetectEnvironment() (priority int, available bool) {
	// Check for AWS credentials in various forms

	// 1. Check for explicit AWS credentials
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return f.Priority(), true
	}

	// 2. Check for AWS profile
	if os.Getenv("AWS_PROFILE") != "" {
		return f.Priority(), true
	}

	// 3. Check if running on AWS (EC2/ECS/Lambda) by looking for instance metadata
	// This is a simplified check - in production you might want to actually try to access the metadata service
	if os.Getenv("AWS_EXECUTION_ENV") != "" || os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		return f.Priority() + 50, true // Higher priority when running on AWS
	}

	// 4. Check for ECS task role
	if os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" {
		return f.Priority() + 50, true
	}

	// 5. Check if ~/.aws/credentials exists
	homeDir, err := os.UserHomeDir()
	if err == nil {
		if _, err := os.Stat(homeDir + "/.aws/credentials"); err == nil {
			return f.Priority(), true
		}
	}

	return 0, false
}

// errorClient is returned when AWS configuration fails
// It allows the provider to be registered but will error on use
type errorClient struct {
	err error
}

func (e *errorClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	return nil, e.err
}
