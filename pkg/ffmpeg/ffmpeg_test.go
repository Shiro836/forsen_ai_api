package ffmpeg_test

import (
	"app/pkg/ffmpeg"
	"context"
	"testing"
	"time"

	_ "embed"

	"github.com/stretchr/testify/require"
)

func TestFfprobePath(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.FfprobePath(ctx, "refs/witcher_low.wav")
	assert.NoError(err)

	assert.NotEmpty(res.Duration)

	// fmt.Println(res)
}

//go:embed refs/witcher_low.wav
var audio []byte

func TestFfprobe(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.Ffprobe(ctx, audio)
	assert.NoError(err)

	assert.NotEmpty(res.Duration)
}
