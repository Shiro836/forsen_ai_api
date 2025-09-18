package s3client

import (
	"context"
	"io"
	"sync"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	UseSSL          bool   `yaml:"use_ssl"`
}

type Client struct {
	cfg            *Config
	minio          *minio.Client
	ensuredBuckets sync.Map
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

	return c, nil
}

func (c *Client) EnsureBucket(ctx context.Context, bucket string) error {
	if _, ok := c.ensuredBuckets.Load(bucket); ok {
		return nil
	}

	exists, err := c.minio.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if !exists {
		if err := c.minio.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return err
		}
	}
	c.ensuredBuckets.Store(bucket, struct{}{})
	return nil
}

func (c *Client) PutObject(ctx context.Context, bucket string, objectName string, reader io.Reader, size int64, contentType string) error {
	if err := c.EnsureBucket(ctx, bucket); err != nil {
		return err
	}
	_, err := c.minio.PutObject(ctx, bucket, objectName, reader, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (c *Client) GetObject(ctx context.Context, bucket string, objectName string) (io.ReadCloser, error) {
	obj, err := c.minio.GetObject(ctx, bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (c *Client) StatObject(ctx context.Context, bucket string, objectName string) (minio.ObjectInfo, error) {
	return c.minio.StatObject(ctx, bucket, objectName, minio.StatObjectOptions{})
}
