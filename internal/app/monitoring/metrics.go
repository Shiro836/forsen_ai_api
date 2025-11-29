package monitoring

import (
	"app/pkg/ai"
	"app/pkg/llm"
	"app/pkg/ws"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	TTSQueryTime       prometheus.Histogram
	TTSErrors          *prometheus.CounterVec
	AIQueryTime        prometheus.Histogram
	AIErrors           *prometheus.CounterVec
	AgenticQueryTime   prometheus.Histogram
	UniversalQueryTime prometheus.Histogram
	AIUserRequests     *prometheus.CounterVec
	NvidiaStats        *prometheus.GaugeVec
}

var AppMetrics = &Metrics{
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
	AgenticQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "agentic",
		Name:      "request_seconds",
	}),
	UniversalQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "universal",
		Name:      "request_seconds",
	}),
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
	ai.RegisterMetrics(reg)
	llm.RegisterMetrics(reg)

	reg.MustRegister(AppMetrics.TTSQueryTime)
	reg.MustRegister(AppMetrics.TTSErrors)
	reg.MustRegister(AppMetrics.AIQueryTime)
	reg.MustRegister(AppMetrics.AIErrors)
	reg.MustRegister(AppMetrics.AgenticQueryTime)
	reg.MustRegister(AppMetrics.UniversalQueryTime)
	reg.MustRegister(AppMetrics.AIUserRequests)
	reg.MustRegister(AppMetrics.NvidiaStats)
}
