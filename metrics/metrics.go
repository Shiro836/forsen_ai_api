package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var WebSocketConnections = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "api",
	Subsystem: "websockets",
	Name:      "conns_total",
})

var TTSQueryTime = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "processor",
	Subsystem: "tts",
	Name:      "request_seconds",
})
var TTSErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "processor",
	Subsystem: "tts",
	Name:      "errors_total",
}, []string{"err_code"})

var AIQueryTime = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "processor",
	Subsystem: "ai",
	Name:      "request_seconds",
})
var AIErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "processor",
	Subsystem: "ai",
	Name:      "errors_total",
}, []string{"err_code"})

var RVCQueryTime = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "processor",
	Subsystem: "rvc",
	Name:      "request_seconds",
})
var RVCErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "processor",
	Subsystem: "rvc",
	Name:      "errors_total",
}, []string{"err_code"})

var AIUserRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "processor",
	Subsystem: "ai",
	Name:      "request_total",
}, []string{"user_name"})

var NvidiaStats = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "system",
	Subsystem: "gpu",
	Name:      "stats_info",
}, []string{"gpu_id", "stat_name"})
