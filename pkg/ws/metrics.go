package ws

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	WebSocketConnections prometheus.Gauge
}

var metrics = &Metrics{
	WebSocketConnections: prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: "websockets",
		Name:      "conns_total",
	}),
}

func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(metrics.WebSocketConnections)
}
