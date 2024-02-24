package main

import (
	"app/ai"
	"app/api"
	postgredb "app/postgre_db"
	"app/rvc"
	"app/tts"
	"app/twitch"
)

type Config struct {
	AI     ai.Config        `yaml:"ai"`
	TTS    tts.Config       `yaml:"tts"`
	Twitch twitch.Config    `yaml:"twitch"`
	Api    api.Config       `yaml:"api"`
	Rvc    rvc.Config       `yaml:"rvc"`
	DB     postgredb.Config `yaml:"db"`
}

type AiConfig struct {
	URL string `yaml:"url"`
}
