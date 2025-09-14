package s3client

import (
	"context"
	"io"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	UseSSL          bool   `yaml:"use_ssl"`
	Bucket          string `yaml:"bucket"`
}

type Client struct {
	cfg   *Config
	minio *minio.Client
}

func New(ctx context.Context, cfg *Config) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, err
	}

	c := &Client{
		cfg:   cfg,
		minio: mc,
	}

	// Ensure bucket exists
	exists, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, err
		}
	}

	return c, nil
}

func (c *Client) PutObject(ctx context.Context, objectName string, reader io.Reader, size int64, contentType string) error {
	_, err := c.minio.PutObject(ctx, c.cfg.Bucket, objectName, reader, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (c *Client) GetObject(ctx context.Context, objectName string) (io.ReadCloser, error) {
	obj, err := c.minio.GetObject(ctx, c.cfg.Bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (c *Client) StatObject(ctx context.Context, objectName string) (minio.ObjectInfo, error) {
	return c.minio.StatObject(ctx, c.cfg.Bucket, objectName, minio.StatObjectOptions{})
}
