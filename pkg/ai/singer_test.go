package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSinger(t *testing.T) (*SingerClient, *int) {
	t.Helper()

	melodyFetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/melodies":
			melodyFetches++
			json.NewEncoder(w).Encode([]Melody{
				{ID: "en021a", Number: 20, Name: "jingle_bells", TotalNotes: 193, DurationS: 76.5},
				{ID: "en013a", Number: 12, Name: "the_finger_familly", TotalNotes: 105, DurationS: 42.6},
			})
		case "/melodies/en021a/audio", "/melodies/20/audio":
			w.Header().Set("Content-Type", "audio/wav")
			w.Write([]byte("RIFFmelody"))
		case "/melodies/nope/audio":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "unknown melody 'nope'"})
		case "/sing":
			var req singRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			if req.Melody == "nope" {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "unknown melody 'nope'"})
				return
			}
			assert.Equal(t, "/tmp/ref.wav", req.SpeakerAudioPath)
			json.NewEncoder(w).Encode(map[string]any{
				"audio": []byte("RIFF"),
				"words": []map[string]any{
					{"word": "jingle", "start": 0.1, "end": 0.6, "aligned": true},
					{"word": "bells", "start": 0.7, "end": 0.98, "aligned": false},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return NewSingerClient(nil, &SingerConfig{URL: srv.URL, Timeout: 5 * time.Second}), &melodyFetches
}

func TestSingerResolveMelody(t *testing.T) {
	client, fetches := newTestSinger(t)
	ctx := context.Background()

	cases := []struct {
		spec string
		want string
		ok   bool
	}{
		{"", MelodyRandom, true},
		{"Random", MelodyRandom, true},
		{"jingle_bells", "jingle_bells", true},
		{"Jingle_Bells", "jingle_bells", true},
		{"20", "jingle_bells", true},
		{"12", "the_finger_familly", true},
		{" 12", "", false},
		{"20 ", "", false},
		{" ", "", false},
		{"99", "", false},
		{"nope", "", false},
	}

	for _, tc := range cases {
		got, err := client.ResolveMelody(ctx, tc.spec)
		assert.Equal(t, tc.want, got, tc.spec)
		if tc.ok {
			assert.NoError(t, err, tc.spec)
		} else {
			assert.ErrorIs(t, err, ErrUnknownMelody, tc.spec)
		}
	}

	assert.Equal(t, len(cases)-2, *fetches, "every non-random lookup fetches the list")

	down := NewSingerClient(nil, &SingerConfig{URL: "http://127.0.0.1:1", Timeout: time.Second})
	_, err := down.ResolveMelody(ctx, "20")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnknownMelody)
}

func TestSingerMelodyAudio(t *testing.T) {
	client, _ := newTestSinger(t)
	ctx := context.Background()

	for _, id := range []string{"en021a", "20"} {
		audio, err := client.MelodyAudio(ctx, id)
		require.NoError(t, err, id)
		assert.Equal(t, []byte("RIFFmelody"), audio, id)
	}

	_, err := client.MelodyAudio(ctx, "nope")
	assert.ErrorIs(t, err, ErrUnknownMelody)
	assert.Contains(t, err.Error(), "unknown melody 'nope'")
}

func TestSingerSing(t *testing.T) {
	client, _ := newTestSinger(t)
	ctx := context.Background()

	audio, timings, err := client.Sing(ctx, "jingle bells", "jingle_bells", "/tmp/ref.wav")
	require.NoError(t, err)
	assert.Equal(t, []byte("RIFF"), audio)
	require.Len(t, timings, 2)
	assert.Equal(t, "jingle", timings[0].Text)
	assert.Equal(t, 100*time.Millisecond, timings[0].Start)
	assert.Equal(t, 980*time.Millisecond, timings[1].End)

	_, _, err = client.Sing(ctx, "x", "nope", "/tmp/ref.wav")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown melody 'nope'")
}
