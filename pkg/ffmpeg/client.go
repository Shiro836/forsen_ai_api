package ffmpeg

import "os"

type Config struct {
	TmpDir string `yaml:"tmp_dir"`
}

type Client struct {
	cfg *Config
}

func New(cfg *Config) *Client {
	return &Client{
		cfg: cfg,
	}
}

func (c *Client) TmpDir() string {
	if c == nil || c.cfg == nil || c.cfg.TmpDir == "" {
		return os.TempDir()
	}
	return c.cfg.TmpDir
}

const prefix = "forsen_"
