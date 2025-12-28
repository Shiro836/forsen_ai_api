package twitch

import (
	"fmt"
	"io"
	"log/slog"
	"testing"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/stretchr/testify/assert"
)

func TestShardedClient_Distribution(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewShardedClient(logger, func(pm gempir.PrivateMessage) {}, func() {}, func() {}, func(string, string) {})

	for i := 0; i < 50; i++ {
		client.Join(fmt.Sprintf("channel%d", i))
	}

	assert.Equal(t, 1, client.ShardCount())

	client.Join("channel50")
	assert.Equal(t, 2, client.ShardCount())

	for i := 51; i < 100; i++ {
		client.Join(fmt.Sprintf("channel%d", i))
	}
	assert.Equal(t, 2, client.ShardCount())

	client.Join("channel100")
	assert.Equal(t, 3, client.ShardCount())

	client.Join("channel0")
	assert.Equal(t, 3, client.ShardCount())
}

func TestShardedClient_Idempotency_Scale(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewShardedClient(logger, func(pm gempir.PrivateMessage) {}, func() {}, func() {}, func(string, string) {})

	for i := 0; i < 150; i++ {
		channelName := fmt.Sprintf("channel%d", i)
		client.Join(channelName)
		client.Join(channelName)
	}

	assert.Equal(t, 3, client.ShardCount())
}

func TestShardedClient_Cleanup(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewShardedClient(logger, func(pm gempir.PrivateMessage) {}, func() {}, func() {}, func(string, string) {})

	for i := 0; i < 51; i++ {
		client.Join(fmt.Sprintf("channel%d", i))
	}
	assert.Equal(t, 2, client.ShardCount())

	client.Depart("channel50")
	assert.Equal(t, 1, client.ShardCount())

	for i := 0; i < 50; i++ {
		client.Depart(fmt.Sprintf("channel%d", i))
	}
	assert.Equal(t, 0, client.ShardCount())
}
