package s3client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Config struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
	UseSSL          bool
	Bucket          string
	Prefix          string
}

type Client struct {
	raw    *s3.Client
	bucket string
	prefix string
}

func New(ctx context.Context, cfg Config) (*Client, error) {
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}

	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if strings.TrimSpace(cfg.AccessKeyID) != "" || strings.TrimSpace(cfg.SecretAccessKey) != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				strings.TrimSpace(cfg.AccessKeyID),
				strings.TrimSpace(cfg.SecretAccessKey),
				"",
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = cfg.ForcePathStyle
		if endpoint := normalizeEndpoint(strings.TrimSpace(cfg.Endpoint), cfg.UseSSL); endpoint != "" {
			options.EndpointResolver = s3.EndpointResolverFromURL(endpoint)
		}
	})

	return &Client{
		raw:    client,
		bucket: strings.TrimSpace(cfg.Bucket),
		prefix: strings.Trim(strings.TrimSpace(cfg.Prefix), "/"),
	}, nil
}

func (c *Client) Raw() *s3.Client {
	if c == nil {
		return nil
	}
	return c.raw
}

func (c *Client) Bucket() string {
	if c == nil {
		return ""
	}
	return c.bucket
}

func (c *Client) Prefix() string {
	if c == nil {
		return ""
	}
	return c.prefix
}

func (c *Client) UploadFile(ctx context.Context, localPath, key string) (string, error) {
	if c == nil || c.raw == nil {
		return "", fmt.Errorf("s3 client is not initialized")
	}
	if strings.TrimSpace(c.bucket) == "" {
		return "", fmt.Errorf("s3 bucket is required")
	}

	path := strings.TrimSpace(localPath)
	if path == "" {
		return "", fmt.Errorf("local file path is required")
	}
	objectKey := strings.Trim(strings.TrimSpace(key), "/")
	if objectKey == "" {
		objectKey = filepath.Base(path)
	}

	reader, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = reader.Close()
	}()

	uploader := manager.NewUploader(c.raw)
	out, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(objectKey),
		Body:   reader,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(aws.ToString(out.ETag)), nil
}

func normalizeEndpoint(endpoint string, useSSL bool) string {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	if useSSL {
		return "https://" + trimmed
	}
	return "http://" + trimmed
}
