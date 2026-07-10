//go:build integration

package ai_test

import (
	"app/pkg/ai"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStyleTTS hits the running StyleTTS2 service on :4111:
//
//	go test -tags integration ./pkg/ai/ -run TestStyleTTS -v
func TestStyleTTS(t *testing.T) {
	assert := require.New(t)

	client := ai.NewStyleTTSClient(http.DefaultClient, &ai.StyleTTSConfig{
		URL: "http://localhost:4111/tts",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, _, err := client.TTS(ctx, "hello world", audioRef)
	assert.NoError(err)
	assert.NotEmpty(res)

	// listenable output for manual checking
	out := filepath.Join(t.TempDir(), "res.wav")
	assert.NoError(os.WriteFile(out, res, 0o644))
	t.Logf("wrote %s", out)
}
