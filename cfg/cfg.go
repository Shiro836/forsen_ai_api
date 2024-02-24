package cfg

import (
	"app/ai_clients/llm"
	"app/ai_clients/rvc"
	"app/ai_clients/tts"
	"app/api"
	"app/db/postgres"
	"app/twitch"
)

type Config struct {
	LLM    llm.Config      `yaml:"llm"`
	TTS    tts.Config      `yaml:"tts"`
	Twitch twitch.Config   `yaml:"twitch"`
	Api    api.Config      `yaml:"api"`
	Rvc    rvc.Config      `yaml:"rvc"`
	DB     postgres.Config `yaml:"db"`
}

type AiConfig struct {
	URL string `yaml:"url"`
}
