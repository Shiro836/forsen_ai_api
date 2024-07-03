package cfg

import (
	"app/db"
	"app/internal/app/api"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/twitch"
)

type Config struct {
	Api api.Config `yaml:"api"`

	LLM      ai.VLLMConfig     `yaml:"llm"`
	MetaTTS  ai.MetaTTSConfig  `yaml:"meta_tts"`
	StyleTTS ai.StyleTTSConfig `yaml:"style_tts"`
	Rvc      ai.RVCConfig      `yaml:"rvc"`
	Whisper  ai.WhisperConfig  `yaml:"whisper"`

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
