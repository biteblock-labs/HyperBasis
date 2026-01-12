package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPrometheusCounters(t *testing.T) {
	prom := NewPrometheus()
	prom.Metrics.OrdersPlaced.Inc()
	prom.Metrics.OrdersFailed.Inc()
	prom.Metrics.EntryFailed.Inc()
	prom.Metrics.ExitFailed.Inc()
	prom.Metrics.KillSwitchEngaged.Inc()
	prom.Metrics.KillSwitchRestored.Inc()

	assertCounter(t, prom.ordersPlaced, 1)
	assertCounter(t, prom.ordersFailed, 1)
	assertCounter(t, prom.entryFailed, 1)
	assertCounter(t, prom.exitFailed, 1)
	assertCounter(t, prom.killEngaged, 1)
	assertCounter(t, prom.killRestored, 1)
}

func assertCounter(t *testing.T, counter prometheus.Counter, expected float64) {
	t.Helper()
	if got := testutil.ToFloat64(counter); got != expected {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}
