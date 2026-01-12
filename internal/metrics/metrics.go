package metrics

type Counter interface {
	Inc()
}

type Metrics struct {
	OrdersPlaced       Counter
	OrdersFailed       Counter
	EntryFailed        Counter
	ExitFailed         Counter
	KillSwitchEngaged  Counter
	KillSwitchRestored Counter
}

type noopCounter struct{}

func (noopCounter) Inc() {}

func NewNoop() *Metrics {
	n := noopCounter{}
	return &Metrics{
		OrdersPlaced:       n,
		OrdersFailed:       n,
		EntryFailed:        n,
		ExitFailed:         n,
		KillSwitchEngaged:  n,
		KillSwitchRestored: n,
	}
}
