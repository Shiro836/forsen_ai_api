package main_test

import (
	"app/db"
	"app/rvc"
	"app/tts"
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func writeFile(fileName string, data []byte) error {
	file, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	_, err = file.Write(data)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}

	return nil
}

func TestTest(t *testing.T) {
	db.InitDB()
	defer db.Close()

	assert := assert.New(t)

	refAudio, err := db.GetVoice("megumin")
	assert.NoError(err)

	ttsClient := tts.New(http.DefaultClient, &tts.Config{
		URL: "http://localhost:4111/tts",
	})

	audio, err := ttsClient.TTS(context.Background(), "forsen forsen forsen forsen forsen forsen forsen. I am fucking whore!!! I am fucking whore???  I just don't like it when people ignore me. ", refAudio)
	assert.NoError(err)

	rvcClient := rvc.New(http.DefaultClient, &rvc.Config{URL: "http://localhost:8001/voice2voice"})

	rvcAudio, err := rvcClient.Rvc(context.Background(), "megumin", audio.Audio, 5)
	assert.NoError(err)

	err = writeFile("megumin_result.wav", rvcAudio)
	assert.NoError(err)
}
