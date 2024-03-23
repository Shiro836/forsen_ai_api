package ai

type WhisperConfig struct {
	URL string
}

type WhisperClient struct {
	httpClient HTTPClient
	cfg        *WhisperConfig
}

func NewWhisperClient(httpClient HTTPClient, cfg *WhisperConfig) *WhisperClient {
	return &WhisperClient{
		httpClient: httpClient,
		cfg:        cfg,
	}
}
