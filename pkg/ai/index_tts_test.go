package ai_test

import (
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIndexTTSClientSynthesizeSuccess(t *testing.T) {
	t.Parallel()

	var observedRequest map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		err = json.Unmarshal(body, &observedRequest)
		require.NoError(t, err)

		require.Equal(t, "hello world", observedRequest["text"])
		require.Equal(t, "assets/jay_promptvn.wav", observedRequest["spk_audio_path"])
		require.EqualValues(t, 0, observedRequest["emo_control_method"])
		require.EqualValues(t, float64(1), observedRequest["emo_weight"])

		vec, ok := observedRequest["emo_vec"].([]any)
		require.True(t, ok)
		require.Equal(t, 8, len(vec))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("RIFF....WAVE"))
	}))
	defer srv.Close()

	client := ai.NewIndexTTSClient(srv.Client(), &ai.IndexTTSConfig{
		URL: srv.URL,
	})

	audio, err := client.Synthesize(context.Background(), &ai.IndexTTS2Request{
		Text:             "hello world",
		SpeakerAudioPath: "assets/jay_promptvn.wav",
	})
	require.NoError(t, err)
	require.Equal(t, []byte("RIFF....WAVE"), audio)
}

func TestIndexTTSClientSynthesizeError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","error":"emo vector sum exceeded"}`))
	}))
	defer srv.Close()

	client := ai.NewIndexTTSClient(srv.Client(), &ai.IndexTTSConfig{
		URL: srv.URL,
	})

	_, err := client.Synthesize(context.Background(), &ai.IndexTTS2Request{
		Text:             "hello world",
		SpeakerAudioPath: "assets/jay_promptvn.wav",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "emo vector sum exceeded")
}

func TestIndexTTSEngineTTS(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ffmpegClient := ffmpeg.New(&ffmpeg.Config{TmpDir: tmpDir})

	expectedTrimmed, err := ffmpegClient.TrimToWav(context.Background(), audioRef, 25*time.Second)
	require.NoError(t, err)
	require.NotEmpty(t, expectedTrimmed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req map[string]any
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		err = json.Unmarshal(body, &req)
		require.NoError(t, err)

		path, ok := req["spk_audio_path"].(string)
		require.True(t, ok)

		require.EqualValues(t, ai.EmoControlMethodText, req["emo_control_method"])

		require.Equal(t, "hello world", req["emo_text"])

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, expectedTrimmed, data)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FAKEAUDIO"))
	}))
	defer srv.Close()

	client := ai.NewIndexTTSClient(srv.Client(), &ai.IndexTTSConfig{
		URL: srv.URL,
	})
	engine := ai.NewIndexTTSEngine(client, ffmpegClient)

	audio, timings, err := engine.TTS(context.Background(), "hello world", audioRef)
	require.NoError(t, err)
	require.Nil(t, timings)
	require.Equal(t, []byte("FAKEAUDIO"), audio)
}
