package monitoring

import (
	"app/pkg/ai"
	"app/pkg/llm"
	"app/pkg/metrics"
	"app/pkg/ws"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	TTSQueryTime       prometheus.Histogram
	ChatTTSQueryTime   prometheus.Histogram
	AIQueryTime        prometheus.Histogram
	AgenticQueryTime   prometheus.Histogram
	UniversalQueryTime prometheus.Histogram
	HandlerErrors      *prometheus.CounterVec
	RewardRedeems      *prometheus.CounterVec
	NvidiaStats        *prometheus.GaugeVec
}

var AppMetrics = &Metrics{
	TTSQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "tts",
		Name:      "request_seconds",
		Buckets:   metrics.RequestSecondsBuckets,
	}),
	ChatTTSQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "chat_tts",
		Name:      "request_seconds",
		Buckets:   metrics.RequestSecondsBuckets,
	}),
	AIQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "ai",
		Name:      "request_seconds",
		Buckets:   metrics.RequestSecondsBuckets,
	}),
	AgenticQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "agentic",
		Name:      "request_seconds",
		Buckets:   metrics.RequestSecondsBuckets,
	}),
	UniversalQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "processor",
		Subsystem: "universal",
		Name:      "request_seconds",
		Buckets:   metrics.RequestSecondsBuckets,
	}),
	HandlerErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "processor",
		Subsystem: "handler",
		Name:      "errors_total",
		Help:      "Failed message-handler runs, per reward flow",
	}, []string{"flow"}),
	RewardRedeems: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "processor",
		Subsystem: "rewards",
		Name:      "redeems_total",
	}, []string{"streamer", "reward_type"}),
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
	reg.MustRegister(AppMetrics.ChatTTSQueryTime)
	reg.MustRegister(AppMetrics.AIQueryTime)
	reg.MustRegister(AppMetrics.AgenticQueryTime)
	reg.MustRegister(AppMetrics.UniversalQueryTime)
	reg.MustRegister(AppMetrics.HandlerErrors)
	reg.MustRegister(AppMetrics.RewardRedeems)
	reg.MustRegister(AppMetrics.NvidiaStats)
}
