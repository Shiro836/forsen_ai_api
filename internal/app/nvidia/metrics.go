package nvidia

import (
	"app/pkg/ws"
	"context"
	"fmt"
	"log/slog"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
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

func GatherAndSendMetrics(ctx context.Context, reg prometheus.Gatherer, influxWriter api.WriteAPI, logger *slog.Logger) {
	metrics, err := reg.Gather()
	if err != nil {
		logger.Error("Error gathering metrics", "err", err)
		return
	}

	for _, m := range metrics {
		for _, metric := range m.GetMetric() {
			select {
			case <-ctx.Done():
				logger.Debug("Context canceled, stopping metric processing")
				return
			default:
				labels := make(map[string]string)
				for _, label := range metric.GetLabel() {
					labels[label.GetName()] = label.GetValue()
				}

				switch m.GetType() {
				case io_prometheus_client.MetricType_COUNTER:
					point := influxdb2.NewPoint(
						m.GetName(),
						labels,
						map[string]interface{}{"value": metric.GetCounter().GetValue()},
						time.Now(),
					)
					influxWriter.WritePoint(point)
				case io_prometheus_client.MetricType_GAUGE:
					point := influxdb2.NewPoint(
						m.GetName(),
						labels,
						map[string]interface{}{"value": metric.GetGauge().GetValue()},
						time.Now(),
					)
					influxWriter.WritePoint(point)
				case io_prometheus_client.MetricType_HISTOGRAM, io_prometheus_client.MetricType_GAUGE_HISTOGRAM:
					hist := metric.GetHistogram()
					for _, bucket := range hist.GetBucket() {
						bucketLabels := make(map[string]string)
						for k, v := range labels {
							bucketLabels[k] = v
						}
						bucketLabels["le"] = fmt.Sprintf("%f", bucket.GetUpperBound())
						point := influxdb2.NewPoint(
							m.GetName()+"_bucket",
							bucketLabels,
							map[string]interface{}{"count": bucket.GetCumulativeCount()},
							time.Now(),
						)
						influxWriter.WritePoint(point)
					}
					influxWriter.WritePoint(influxdb2.NewPoint(
						m.GetName()+"_sum",
						labels,
						map[string]interface{}{"value": hist.GetSampleSum()},
						time.Now(),
					))
					influxWriter.WritePoint(influxdb2.NewPoint(
						m.GetName()+"_count",
						labels,
						map[string]interface{}{"value": hist.GetSampleCount()},
						time.Now(),
					))
				default:
					logger.Error("Unsupported metric type", "err", m.GetType().String())
				}
			}
		}
	}
}
