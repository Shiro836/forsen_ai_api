package ffmpeg

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

const prefix = "forsen_"
