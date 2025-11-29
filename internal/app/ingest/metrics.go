package ingest

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	ActiveChannels       prometheus.Gauge
	TotalGrantedChannels prometheus.Gauge
	MessagesIngested     prometheus.Counter
	ConnectedClients     prometheus.Gauge
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
}

func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(metrics.ActiveChannels)
	reg.MustRegister(metrics.TotalGrantedChannels)
	reg.MustRegister(metrics.MessagesIngested)
	reg.MustRegister(metrics.ConnectedClients)
}
