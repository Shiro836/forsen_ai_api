package ai

import (
	appmetrics "app/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	TTSQueryTime prometheus.Histogram
	TTSErrors    *prometheus.CounterVec
}

var metrics = &Metrics{
	TTSQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: "tts",
		Name:      "request_seconds",
		Buckets:   appmetrics.RequestSecondsBuckets,
	}),
	TTSErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "tts",
		Name:      "errors_total",
	}, []string{"err_code"}),
}

func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(metrics.TTSQueryTime)
	reg.MustRegister(metrics.TTSErrors)
}
