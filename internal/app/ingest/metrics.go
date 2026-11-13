package ingest

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	ActiveChannels       prometheus.Gauge
	TotalGrantedChannels prometheus.Gauge
	JoinedChannels       prometheus.Gauge
	JoinFailures         *prometheus.CounterVec
	MessagesIngested     prometheus.Counter
	ConnectedClients     prometheus.Gauge
	ShardCount           prometheus.Gauge

	EventSubSubscriptions     prometheus.Gauge
	EventSubSubscribeFailures *prometheus.CounterVec
	EventSubNotifications     *prometheus.CounterVec
}

var metrics = &Metrics{
	ActiveChannels: prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "twitch_ingest",
		Subsystem: "channels",
		Name:      "active_count",
		Help:      "Number of currently active Twitch channels being listened to",
	}),
	TotalGrantedChannels: prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "twitch_ingest",
		Subsystem: "channels",
		Name:      "total_granted_count",
		Help:      "Total number of users with streamer permission granted in DB",
	}),
	JoinedChannels: prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "twitch_ingest",
		Subsystem: "channels",
		Name:      "joined_count",
		Help:      "Number of Twitch channels we've actually joined (confirmed via IRC self JOIN)",
	}),
	JoinFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "twitch_ingest",
		Subsystem: "channels",
		Name:      "join_failures_total",
		Help:      "Number of Twitch channel join failures (NOTICE msg-id), labeled by reason",
	}, []string{"reason"}),
	MessagesIngested: prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "twitch_ingest",
		Subsystem: "messages",
		Name:      "ingested_total",
		Help:      "Total number of Twitch messages ingested",
	}),
	ConnectedClients: prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "twitch_ingest",
		Subsystem: "clients",
		Name:      "connected_count",
		Help:      "Number of connected Twitch clients",
	}),
	ShardCount: prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "twitch_ingest",
		Subsystem: "shards",
		Name:      "active_count",
		Help:      "Number of active Twitch IRC shards (connections managed by the sharded client)",
	}),
	EventSubSubscriptions: prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "twitch_ingest",
		Subsystem: "eventsub",
		Name:      "subscriptions_count",
		Help:      "Number of enabled EventSub subscriptions on our conduit",
	}),
	EventSubSubscribeFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "twitch_ingest",
		Subsystem: "eventsub",
		Name:      "subscribe_failures_total",
		Help:      "Number of EventSub subscriptions Twitch refused, labeled by event type",
	}, []string{"type"}),
	EventSubNotifications: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "twitch_ingest",
		Subsystem: "eventsub",
		Name:      "notifications_total",
		Help:      "Number of EventSub notifications received, labeled by event type",
	}, []string{"type"}),
}

func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(metrics.ActiveChannels)
	reg.MustRegister(metrics.TotalGrantedChannels)
	reg.MustRegister(metrics.JoinedChannels)
	reg.MustRegister(metrics.JoinFailures)
	reg.MustRegister(metrics.MessagesIngested)
	reg.MustRegister(metrics.ConnectedClients)
	reg.MustRegister(metrics.ShardCount)
	reg.MustRegister(metrics.EventSubSubscriptions)
	reg.MustRegister(metrics.EventSubSubscribeFailures)
	reg.MustRegister(metrics.EventSubNotifications)
}
