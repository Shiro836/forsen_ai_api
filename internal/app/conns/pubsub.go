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
	// BlockPublishUntilSubscriberAck is what makes delivery ordered: without it
	// gochannel spawns a goroutine per message and back-to-back publishes race
	// into the subscriber (audio chunks played/captioned out of order). Every
	// subscriber adapter acks immediately after a drop-on-full forward, so
	// blocking costs nothing.
	ps := gochannel.NewGoChannel(gochannel.Config{
		OutputChannelBuffer:            256,
		Persistent:                     false,
		BlockPublishUntilSubscriberAck: true,
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
