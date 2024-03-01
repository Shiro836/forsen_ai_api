package ai

import (
	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	TTSQueryTime   prometheus.Histogram
	TTSErrors      *prometheus.CounterVec
	LLMQueryTime   prometheus.Histogram
	LLMErrors      *prometheus.CounterVec
	RVCQueryTime   prometheus.Histogram
	RVCErrors      *prometheus.CounterVec
	AIUserRequests *prometheus.CounterVec
}

var metrics = &Metrics{
	TTSQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: "tts",
		Name:      "request_seconds",
	}),
	TTSErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "tts",
		Name:      "errors_total",
	}, []string{"err_code"}),
	LLMQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: "llm",
		Name:      "request_seconds",
	}),
	LLMErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "llm",
		Name:      "errors_total",
	}, []string{"err_code"}),
	RVCQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: "rvc",
		Name:      "request_seconds",
	}),
	RVCErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "rvc",
		Name:      "errors_total",
	}, []string{"err_code"}),
	AIUserRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "ai",
		Name:      "request_total",
	}, []string{"user_name"}),
}

func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(metrics.TTSQueryTime)
	reg.MustRegister(metrics.TTSErrors)
	reg.MustRegister(metrics.LLMQueryTime)
	reg.MustRegister(metrics.LLMErrors)
	reg.MustRegister(metrics.RVCQueryTime)
	reg.MustRegister(metrics.RVCErrors)
	reg.MustRegister(metrics.AIUserRequests)
}
