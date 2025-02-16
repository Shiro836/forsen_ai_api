package cfg

import (
	"app/db"
	"app/internal/app/api"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/twitch"
	"app/pkg/whisperx"
)

type Config struct {
	Api api.Config `yaml:"api"`

	LLM      llm.Config        `yaml:"llm"`
	StyleTTS ai.StyleTTSConfig `yaml:"style_tts"`
	Whisper  whisperx.Config   `yaml:"whisper"`

	Twitch twitch.Config `yaml:"twitch"`

	DB db.Config `yaml:"db"`

	InfluxDB InfluxConfig `yaml:"influx"`

	Ffmpeg ffmpeg.Config `yaml:"ffmpeg"`
}

type InfluxConfig struct {
	URL    string `yaml:"url"`
	Token  string `yaml:"token"`
	Org    string `yaml:"org"`
	Bucket string `yaml:"bucket"`
}
