package immediateticker

import "time"

type ImmediateTicker struct {
	C chan time.Time
	t *time.Ticker
}

func New(interval time.Duration) *ImmediateTicker {
	aggregated := make(chan time.Time)
	t := time.NewTicker(interval)

	go func() {
		for tickTime := range t.C {
			aggregated <- tickTime
		}
	}()

	go func() {
		aggregated <- time.Now()
	}()

	return &ImmediateTicker{
		C: aggregated,
		t: t,
	}
}

func (it *ImmediateTicker) Stop() {
	it.t.Stop()
}

func (it *ImmediateTicker) Reset(interval time.Duration) {
	it.t.Reset(interval)
}
