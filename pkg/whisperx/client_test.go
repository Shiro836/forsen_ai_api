package whisperx_test

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"testing"
	"time"

	"app/pkg/ffmpeg"
	"app/pkg/whisperx"

	"github.com/stretchr/testify/require"
)

//go:embed refs/witcher_low.wav
var audio []byte

func TestAlign(t *testing.T) {
	assert := require.New(t)

	transcript := "Likely to get more patrons now. A bit more like to be useful. Here? A crossbow? A giant this close to human settlements. Strange. A hundred. A key?"

	client := whisperx.New(http.DefaultClient, &whisperx.Config{
		URL: "http://localhost:8777",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ffmpeg := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	res, err := ffmpeg.Ffprobe(ctx, audio)
	assert.NoError(err)

	timings, err := client.Align(ctx, transcript, audio, res.Duration)
	assert.NoError(err)

	fmt.Println(timings)

	assert.NotEqual(0, len(timings))
}
