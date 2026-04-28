package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/truvaagents/truva-g3/telemetry"
)

// S3Client handles all S3/MinIO operations: upload, download, listing, and pre-signed URLs.
type S3Client struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
	endpoint      string
}

// NewS3Client creates an S3 client configured for MinIO or AWS S3.
// When S3_ENDPOINT is set, uses path-style addressing for MinIO compatibility.
// When S3_ENDPOINT is empty, uses standard AWS SDK credential chain and virtual-hosted-style.
func NewS3Client(endpoint, bucket, accessKey, secretKey, region string) (*S3Client, error) {
	var cfg aws.Config
	var err error

	useCustomEndpoint := endpoint != ""

	// Use traced HTTP client for OTel span propagation on S3 operations
	tracedHTTPClient := telemetry.NewTracedHTTPClient(nil)

	if useCustomEndpoint {
		// MinIO / S3-compatible: custom endpoint with static credentials
		customResolver := aws.EndpointResolverWithOptionsFunc(
			func(service, resolvedRegion string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               endpoint,
					HostnameImmutable: true, // Path-style for MinIO
				}, nil
			},
		)
		cfg, err = config.LoadDefaultConfig(context.Background(),
			config.WithRegion(region),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
			config.WithEndpointResolverWithOptions(customResolver),
			config.WithHTTPClient(tracedHTTPClient),
		)
	} else {
		// Real AWS S3: use SDK default credential chain (env vars, IAM role, instance profile)
		// Static credentials are used if S3_ACCESS_KEY/S3_SECRET_KEY are provided;
		// otherwise the SDK discovers credentials from the environment automatically.
		opts := []func(*config.LoadOptions) error{
			config.WithRegion(region),
			config.WithHTTPClient(tracedHTTPClient),
		}
		if accessKey != "" && secretKey != "" {
			opts = append(opts, config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
			))
		}
		cfg, err = config.LoadDefaultConfig(context.Background(), opts...)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if useCustomEndpoint {
			o.UsePathStyle = true // Required for MinIO path-style addressing
		}
		// Real AWS S3 uses virtual-hosted-style by default — no override needed
	})

	return &S3Client{
		client:        client,
		presignClient: s3.NewPresignClient(client),
		bucket:        bucket,
		endpoint:      endpoint,
	}, nil
}

// UploadFile uploads a single file to S3
func (c *S3Client) UploadFile(ctx context.Context, key string, filePath string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat file %s: %w", filePath, err)
	}

	contentType := inferContentType(filePath)

	_, err = c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        file,
		ContentType: &contentType,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to upload to S3 key %s: %w", key, err)
	}

	return stat.Size(), nil
}

// UploadContent uploads string content to S3
func (c *S3Client) UploadContent(ctx context.Context, key string, content string, contentType string) error {
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &c.bucket,
		Key:         &key,
		Body:        strings.NewReader(content),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("failed to upload content to S3 key %s: %w", key, err)
	}
	return nil
}

// UploadDirectory uploads all files in a directory to S3 under a given prefix
func (c *S3Client) UploadDirectory(ctx context.Context, prefix string, dir string) ([]ArtifactInfo, error) {
	var artifacts []ArtifactInfo

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		key := prefix + "/" + relPath
		size, uploadErr := c.UploadFile(ctx, key, path)
		if uploadErr != nil {
			return uploadErr
		}

		artifacts = append(artifacts, ArtifactInfo{
			Type:      inferArtifactType(relPath),
			Name:      filepath.Base(relPath),
			SizeBytes: size,
			S3Key:     key,
		})

		return nil
	})

	return artifacts, err
}

// GeneratePresignedURL creates a time-limited GET URL for an S3 object
func (c *S3Client) GeneratePresignedURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	presignResult, err := c.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("failed to generate pre-signed URL for %s: %w", key, err)
	}
	return presignResult.URL, nil
}

// ListObjects lists all objects under a given prefix
func (c *S3Client) ListObjects(ctx context.Context, prefix string) ([]ArtifactInfo, error) {
	var artifacts []ArtifactInfo

	input := &s3.ListObjectsV2Input{
		Bucket: &c.bucket,
		Prefix: &prefix,
	}

	output, err := c.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to list objects with prefix %s: %w", prefix, err)
	}

	for _, obj := range output.Contents {
		key := *obj.Key
		artifacts = append(artifacts, ArtifactInfo{
			Type:      inferArtifactType(key),
			Name:      filepath.Base(key),
			SizeBytes: *obj.Size,
			S3Key:     key,
		})
	}

	return artifacts, nil
}

// GetContent downloads an object's content as a string
func (c *S3Client) GetContent(ctx context.Context, key string) (string, error) {
	output, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get S3 object %s: %w", key, err)
	}
	defer output.Body.Close()

	data, err := io.ReadAll(output.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read S3 object %s: %w", key, err)
	}
	return string(data), nil
}

// inferContentType returns a MIME type based on file extension
func inferContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".zip":
		return "application/zip"
	case ".json":
		return "application/json"
	case ".ts":
		return "text/typescript"
	case ".html":
		return "text/html"
	default:
		return "application/octet-stream"
	}
}

// inferArtifactType categorizes an artifact based on its path/name
func inferArtifactType(path string) string {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "screenshot") || strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") {
		return "screenshot"
	}
	if strings.Contains(lower, "trace") || strings.HasSuffix(lower, ".zip") {
		return "trace"
	}
	if strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".js") {
		return "script"
	}
	return "other"
}
