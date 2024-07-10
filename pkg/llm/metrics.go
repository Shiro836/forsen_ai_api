package llm

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	LLMQueryTime prometheus.Histogram
	LLMErrors    *prometheus.CounterVec
}

var metrics = &Metrics{
	LLMQueryTime: prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: "llm",
		Name:      "request_seconds",
	}),
	LLMErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "llm",
		Name:      "errors_total",
	}, []string{"err_code"}),
}

func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(metrics.LLMQueryTime)
	reg.MustRegister(metrics.LLMErrors)
}
