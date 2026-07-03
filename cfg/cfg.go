package cfg

import (
	"app/db"
	"app/internal/app/api"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/s3client"
	"app/pkg/twitch"
	"app/pkg/whisperx"
)

type ClankerConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	AccessToken  string `yaml:"access_token"`
	RefreshToken string `yaml:"refresh_token"`
	BotUserID    string `yaml:"bot_user_id"`
	BotLogin     string `yaml:"bot_login"`
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`

	LangSearchAPIKey string `yaml:"langsearch_api_key"`
}

type Config struct {
	Api     api.Config    `yaml:"api"`
	Ingest  IngestConfig  `yaml:"ingest"`
	Clanker ClankerConfig `yaml:"clanker"`

	LLM        llm.Config        `yaml:"llm"`
	LLM2       llm.Config        `yaml:"llm2"`
	AgenticLLM llm.Config        `yaml:"agentic_llm"`
	ImageLLM   llm.Config        `yaml:"image_llm"`
	// NativeImages sends user images to the character model directly instead
	// of injecting an ImageLLM-written description into the message text.
	NativeImages bool `yaml:"native_images"`
	OAI        llm.Config        `yaml:"oai"`
	StyleTTS   ai.StyleTTSConfig `yaml:"tts"`
	IndexTTS   ai.IndexTTSConfig `yaml:"index_tts"`
	Whisper    whisperx.Config   `yaml:"whisper"`

	Twitch twitch.Config `yaml:"twitch"`

	DB db.Config `yaml:"db"`

	InfluxDB InfluxConfig `yaml:"influx"`

	Ffmpeg ffmpeg.Config `yaml:"ffmpeg"`

	S3 s3client.Config `yaml:"s3"`
}

type IngestConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type InfluxConfig struct {
	URL    string `yaml:"url"`
	Token  string `yaml:"token"`
	Org    string `yaml:"org"`
	Bucket string `yaml:"bucket"`
}
