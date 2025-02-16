package pubsub_test

import (
	"app/pkg/pubsub"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPubSub(t *testing.T) {
	assert := require.New(t)

	pubsub := pubsub.New()

	for topicInt := range 100 {
		topic := strconv.Itoa(topicInt)

		recieved := atomic.Int64{}

		for range 1000 {
			_ = pubsub.Subscribe(topic, func(message any) {
				recieved.Add(1)
			})
		}

		for j := range 1000 {
			pubsub.Publish(topic, j)
		}

		assert.Equal(int64(1000*1000), recieved.Load())
	}
}
