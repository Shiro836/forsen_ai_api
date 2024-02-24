package whisperx

type Config struct {
	URL string
}

type Client struct {
	cfg *Config
}

func NewClient(cfg *Config) *Client {
	return &Client{
		cfg: cfg,
	}
}
