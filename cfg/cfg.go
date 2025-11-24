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

type Config struct {
	Api api.Config `yaml:"api"`

	LLM      llm.Config        `yaml:"llm"`
	ImageLLM llm.Config        `yaml:"image_llm"`
	StyleTTS ai.StyleTTSConfig `yaml:"tts"`
	IndexTTS ai.IndexTTSConfig `yaml:"index_tts"`
	Whisper  whisperx.Config   `yaml:"whisper"`

	Twitch twitch.Config `yaml:"twitch"`

	DB db.Config `yaml:"db"`

	InfluxDB InfluxConfig `yaml:"influx"`

	Ffmpeg ffmpeg.Config `yaml:"ffmpeg"`

	S3 s3client.Config `yaml:"s3"`
}

type InfluxConfig struct {
	URL    string `yaml:"url"`
	Token  string `yaml:"token"`
	Org    string `yaml:"org"`
	Bucket string `yaml:"bucket"`
}
