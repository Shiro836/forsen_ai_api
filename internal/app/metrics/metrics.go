package metrics

import (
	"app/pkg/ws"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	TTSQueryTime   prometheus.Histogram
	TTSErrors      *prometheus.CounterVec
	AIQueryTime    prometheus.Histogram
	AIErrors       *prometheus.CounterVec
	RVCQueryTime   prometheus.Histogram
	RVCErrors      *prometheus.CounterVec
	AIUserRequests *prometheus.CounterVec
	NvidiaStats    *prometheus.GaugeVec
}

var metrics = &Metrics{
	TTSQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "tts",
		Name:      "request_seconds",
	}),
	TTSErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "processor",
		Subsystem: "tts",
		Name:      "errors_total",
	}, []string{"err_code"}),
	AIQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "ai",
		Name:      "request_seconds",
	}),
	AIErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "processor",
		Subsystem: "ai",
		Name:      "errors_total",
	}, []string{"err_code"}),
	RVCQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "rvc",
		Name:      "request_seconds",
	}),
	RVCErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "processor",
		Subsystem: "rvc",
		Name:      "errors_total",
	}, []string{"err_code"}),
	AIUserRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "processor",
		Subsystem: "ai",
		Name:      "request_total",
	}, []string{"user_name"}),
	NvidiaStats: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "system",
		Subsystem: "gpu",
		Name:      "stats_info",
	}, []string{"gpu_id", "stat_name"}),
}

func RegisterMetrics(reg prometheus.Registerer) {
	ws.RegisterMetrics(reg)

	reg.MustRegister(metrics.TTSQueryTime)
	reg.MustRegister(metrics.TTSErrors)
	reg.MustRegister(metrics.AIQueryTime)
	reg.MustRegister(metrics.AIErrors)
	reg.MustRegister(metrics.RVCQueryTime)
	reg.MustRegister(metrics.RVCErrors)
	reg.MustRegister(metrics.AIUserRequests)
	reg.MustRegister(metrics.NvidiaStats)
}
