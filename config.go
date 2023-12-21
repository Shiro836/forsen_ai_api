package main

import (
	"app/ai"
	"app/api"
	"app/rvc"
	"app/tts"
	"app/twitch"
)

type Config struct {
	AI     ai.Config     `yaml:"ai"`
	TTS    tts.Config    `yaml:"tts"`
	Twitch twitch.Config `yaml:"twitch"`
	Api    api.Config    `yaml:"api"`
	Lua    LuaConfig     `yaml:"lua"`
	Rvc    rvc.Config    `yaml:"rvc"`
}

type AiConfig struct {
	URL string `yaml:"url"`
}
