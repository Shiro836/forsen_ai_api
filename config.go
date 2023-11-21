package main

import (
	"app/ai"
	"app/api"
	"app/tts"
	"app/twitch"
)

type Config struct {
	AI     ai.Config     `yaml:"ai"`
	TTS    tts.Config    `yaml:"tts"`
	Twitch twitch.Config `yaml:"twitch"`
	Api    api.Config    `yaml:"api"`
	Lua    LuaConfig     `yaml:"lua"`
}

type AiConfig struct {
	URL string `yaml:"url"`
}
