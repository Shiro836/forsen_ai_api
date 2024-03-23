package slg

import (
	"context"
	"log/slog"

	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	slogcommon "github.com/samber/slog-common"
)

var _ slog.Handler = (*InfluxDBHandler)(nil)

type InfluxDBHandler struct {
	InfluxDBWriter api.WriteAPI

	attrs  []slog.Attr
	groups []string
}

func (h *InfluxDBHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *InfluxDBHandler) Handle(ctx context.Context, record slog.Record) error {
	fields := make(map[string]any, record.NumAttrs()+1)

	record.Attrs(func(a slog.Attr) bool {
		fields[a.Key] = a.Value.Any()

		return true
	})

	fields["message"] = record.Message

	point := write.NewPoint("syslog", map[string]string{
		"level": record.Level.String(),
	}, fields, record.Time)

	h.InfluxDBWriter.WritePoint(point)

	return nil
}

func (h *InfluxDBHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &InfluxDBHandler{
		InfluxDBWriter: h.InfluxDBWriter,

		attrs:  slogcommon.AppendAttrsToGroup(h.groups, h.attrs, attrs...),
		groups: h.groups,
	}
}

func (h *InfluxDBHandler) WithGroup(name string) slog.Handler {
	return &InfluxDBHandler{
		InfluxDBWriter: h.InfluxDBWriter,

		attrs:  h.attrs,
		groups: append(h.groups, name),
	}
}
