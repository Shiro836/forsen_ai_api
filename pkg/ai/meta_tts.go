package ai

type MetaTTSConfig struct {
	URL string `yaml:"url"`
}

type MetaTTSClient struct {
	cfg        *MetaTTSConfig
	httpClient HTTPClient
}

func NewMetaTTSClient(httpClient HTTPClient, cfg *MetaTTSConfig) *MetaTTSClient {
	return &MetaTTSClient{
		httpClient: httpClient,
		cfg:        cfg,
	}
}
