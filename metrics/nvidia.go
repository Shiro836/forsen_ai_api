package metrics

import (
	"app/slg"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const monitorFrequency = time.Second

type param struct {
	paramName string
	action    func(string)
}

func monitorNvidia(ctx context.Context) error {
	gpuName := "unknown"

	params := []param{
		{
			paramName: "gpu_name",
			action: func(value string) {
				gpuName = value
			},
		},
		{
			paramName: "uuid",
			action: func(value string) {
				gpuName += " " + value
			},
		},
		{
			paramName: "memory.used",
			action: func(value string) {
				val, err := strconv.Atoi(value)
				if err != nil {
					return
				}

				NvidiaStats.WithLabelValues(gpuName, "memory_used").Set(float64(val))
			},
		},
		{
			paramName: "memory.total",
			action: func(value string) {
				val, err := strconv.Atoi(value)
				if err != nil {
					return
				}

				NvidiaStats.WithLabelValues(gpuName, "memory_total").Set(float64(val))
			},
		},
		{
			paramName: "temperature.gpu",
			action: func(value string) {
				val, err := strconv.Atoi(value)
				if err != nil {
					return
				}

				NvidiaStats.WithLabelValues(gpuName, "temperature_gpu").Set(float64(val))
			},
		},
		{
			paramName: "power.draw",
			action: func(value string) {
				val, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return
				}

				NvidiaStats.WithLabelValues(gpuName, "power_draw").Set(float64(val))
			},
		},
	}

	queryString := ""
	for i, param := range params {
		if i != 0 {
			queryString += ","
		}
		queryString += param.paramName
	}

	cmd := exec.CommandContext(ctx,
		"nvidia-smi",
		"--query-gpu", queryString,
		"--format", "csv,nounits",
	)

	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to nvidia-smi: %w, errb: %s", err, errb.String())
	}

	rows := strings.Split(outb.String(), "\n")

	for i := 1; i < len(rows); i++ {
		row := rows[i]

		if len(row) == 0 {
			continue
		}

		stats := strings.Split(row, ",")
		if len(stats) != len(params) {
			return fmt.Errorf("got %d stats, expected %d", len(stats), len(params))
		}

		for i := range stats {
			params[i].action(strings.TrimSpace(stats[i]))
		}
	}

	return nil
}

func NvidiaMonitoringLoop(ctx context.Context) {
	ticker := time.NewTicker(monitorFrequency)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ticker.C:
			if err := monitorNvidia(ctx); err != nil {
				slg.GetSlog(ctx).Error("failed to monitor nvidia", "err", err)
			}
		case <-ctx.Done():
			break loop
		}
	}
}
