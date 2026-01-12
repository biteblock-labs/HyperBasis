package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const promNamespace = "hl_carry_bot"

type promCounter struct {
	counter prometheus.Counter
}

func (p promCounter) Inc() {
	p.counter.Inc()
}

type Prometheus struct {
	Metrics *Metrics

	registry     *prometheus.Registry
	ordersPlaced prometheus.Counter
	ordersFailed prometheus.Counter
	entryFailed  prometheus.Counter
	exitFailed   prometheus.Counter
	killEngaged  prometheus.Counter
	killRestored prometheus.Counter
}

func NewPrometheus() *Prometheus {
	registry := prometheus.NewRegistry()
	ordersPlaced := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "orders_placed_total",
		Help:      "Total number of orders placed.",
	})
	ordersFailed := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "orders_failed_total",
		Help:      "Total number of order placement failures.",
	})
	entryFailed := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "entry_failed_total",
		Help:      "Total number of entry flow failures.",
	})
	exitFailed := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "exit_failed_total",
		Help:      "Total number of exit flow failures.",
	})
	killEngaged := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "kill_switch_engaged_total",
		Help:      "Total number of connectivity kill switch engagements.",
	})
	killRestored := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: promNamespace,
		Name:      "kill_switch_restored_total",
		Help:      "Total number of connectivity kill switch recoveries.",
	})

	registry.MustRegister(ordersPlaced, ordersFailed, entryFailed, exitFailed, killEngaged, killRestored)

	m := &Metrics{
		OrdersPlaced:       promCounter{ordersPlaced},
		OrdersFailed:       promCounter{ordersFailed},
		EntryFailed:        promCounter{entryFailed},
		ExitFailed:         promCounter{exitFailed},
		KillSwitchEngaged:  promCounter{killEngaged},
		KillSwitchRestored: promCounter{killRestored},
	}

	return &Prometheus{
		Metrics:      m,
		registry:     registry,
		ordersPlaced: ordersPlaced,
		ordersFailed: ordersFailed,
		entryFailed:  entryFailed,
		exitFailed:   exitFailed,
		killEngaged:  killEngaged,
		killRestored: killRestored,
	}
}

func (p *Prometheus) Handler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}
