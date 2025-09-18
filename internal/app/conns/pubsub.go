package conns

import (
	"context"

	watermill "github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	gochannel "github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
)

type Watermill struct {
	pub message.Publisher
	sub message.Subscriber
}

func NewWatermill() *Watermill {
	logger := watermill.NewStdLogger(false, false)
	ps := gochannel.NewGoChannel(gochannel.Config{
		OutputChannelBuffer:            256,
		Persistent:                     false,
		BlockPublishUntilSubscriberAck: false,
	}, logger)

	return &Watermill{pub: ps, sub: ps}
}

func (w *Watermill) Publish(_ context.Context, topic string, payload []byte) error {
	msg := message.NewMessage(watermill.NewUUID(), payload)
	return w.pub.Publish(topic, msg)
}

func (w *Watermill) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	return w.sub.Subscribe(ctx, topic)
}
