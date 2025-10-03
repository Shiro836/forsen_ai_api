package ai_test

import (
	"app/pkg/ai"
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	_ "embed"

	"github.com/stretchr/testify/require"
)

//go:embed refs/okayeg_ref.wav
var audioRef []byte

func TestStyleTTS(t *testing.T) {
	assert := require.New(t)

	client := ai.NewStyleTTSClient(http.DefaultClient, &ai.StyleTTSConfig{
		URL: "http://localhost:4111/tts",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	res, _, err := client.TTS(ctx, "hello world", audioRef)
	assert.NoError(err)

	err = os.WriteFile("res/res.wav", res, 0644)
	assert.NoError(err)
}
