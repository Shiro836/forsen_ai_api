package ai

import (
	appmetrics "app/pkg/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	TTSQueryTime  prometheus.Histogram
	TTSErrors     *prometheus.CounterVec
	SingQueryTime prometheus.Histogram
	SingErrors    *prometheus.CounterVec
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
	SingQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: "sing",
		Name:      "request_seconds",
		Buckets:   appmetrics.RequestSecondsBuckets,
	}),
	SingErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "sing",
		Name:      "errors_total",
	}, []string{"err_code"}),
}

func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(metrics.TTSQueryTime)
	reg.MustRegister(metrics.TTSErrors)
	reg.MustRegister(metrics.SingQueryTime)
	reg.MustRegister(metrics.SingErrors)
}
