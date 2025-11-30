package twitch

import (
	"log/slog"
	"strings"
	"sync"

	gempir "github.com/gempir/go-twitch-irc/v4"
)

const maxChannelsPerShard = 50

type ShardedClient struct {
	shards            []*Shard
	lock              sync.RWMutex
	logger            *slog.Logger
	onMessage         func(gempir.PrivateMessage)
	onShardConnect    func()
	onShardDisconnect func()
	nextShardID       int
}

func NewShardedClient(logger *slog.Logger, onMessage func(gempir.PrivateMessage), onShardConnect func(), onShardDisconnect func()) *ShardedClient {
	return &ShardedClient{
		shards:            make([]*Shard, 0),
		logger:            logger,
		onMessage:         onMessage,
		onShardConnect:    onShardConnect,
		onShardDisconnect: onShardDisconnect,
		nextShardID:       0,
	}
}

func (c *ShardedClient) Join(channel string) {
	channel = strings.ToLower(channel)
	c.lock.Lock()
	defer c.lock.Unlock()

	for _, shard := range c.shards {
		if shard.HasChannel(channel) {
			return
		}
	}

	for _, shard := range c.shards {
		if shard.Count() < maxChannelsPerShard {
			shard.Join(channel)
			return
		}
	}

	shardID := c.nextShardID
	c.nextShardID++
	newShard := NewShard(shardID, c.logger, c.onMessage, c.onShardConnect, c.onShardDisconnect)
	newShard.Connect()
	newShard.Join(channel)
	c.shards = append(c.shards, newShard)
	c.logger.Info("spawned new shard", "shard_id", shardID)
}

func (c *ShardedClient) Depart(channel string) {
	channel = strings.ToLower(channel)
	c.lock.Lock()
	defer c.lock.Unlock()

	for i, shard := range c.shards {
		if shard.Depart(channel) {
			if shard.Count() == 0 {
				shard.Disconnect()

				c.shards = append(c.shards[:i], c.shards[i+1:]...)
				c.logger.Info("removed empty shard", "shard_id", shard.id)
			}
			return
		}
	}
}

func (c *ShardedClient) Close() {
	c.lock.Lock()
	defer c.lock.Unlock()

	for _, shard := range c.shards {
		shard.Disconnect()
	}
	c.shards = make([]*Shard, 0)
}

func (c *ShardedClient) ShardCount() int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return len(c.shards)
}
