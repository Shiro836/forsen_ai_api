package tts_test

// import (
// 	"app/tts"
// 	"context"
// 	"net/http"
// 	"os"
// 	"testing"

// 	"github.com/stretchr/testify/assert"
// )

// func TestTTS(t *testing.T) {
// 	assert := assert.New(t)

// 	file, err := os.ReadFile("forsen_4.wav")
// 	assert.NoError(err)

// 	client := tts.New(http.DefaultClient, &tts.Config{
// 		URL: "http://192.168.2.177:4111/tts",
// 	})

// 	resp, err := client.TTS(context.Background(), "forsen. forsen. forsen. forsen. forsen. forsen. forsen. forsen. forsen. forsen. ", file)
// 	assert.NoError(err)

// 	err = os.WriteFile("result.wav", resp, 0o666)
// 	assert.NoError(err)
// }

// func TestForsenTTS(t *testing.T) {
// 	assert := assert.New(t)

// 	client := tts.New(http.DefaultClient, &tts.Config{
// 		URL: "http://192.168.2.177:4111/tts",
// 	})

// 	resp, err := client.TTS(context.Background(), "I would like to call my mom because I can't complete this game, because I am not a god gamer, I am a pathetic loser who doesn't eat sugar.", nil)
// 	assert.NoError(err)

// 	err = os.WriteFile("result.wav", resp, 0o666)
// 	assert.NoError(err)
// }
