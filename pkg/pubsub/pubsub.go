package pubsub

import "sync"

type PubSub struct {
	topics map[string]*topic

	lock sync.Mutex
}

func New() *PubSub {
	return &PubSub{
		topics: make(map[string]*topic),
	}
}

func (p *PubSub) initTopic(topic string) {
	if _, ok := p.topics[topic]; !ok {
		p.topics[topic] = newTopic()
	}
}

func (p *PubSub) Publish(topic string, message any) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.initTopic(topic)

	p.topics[topic].publish(message)
}

func (p *PubSub) Subscribe(topic string, handler func(message any)) (unsub func()) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.initTopic(topic)

	return p.topics[topic].subscribe(handler)
}
