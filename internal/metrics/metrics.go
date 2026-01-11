package metrics

type Counter interface {
	Inc()
}

type Metrics struct {
	OrdersPlaced Counter
	OrdersFailed Counter
}

type noopCounter struct{}

func (noopCounter) Inc() {}

func NewNoop() *Metrics {
	n := noopCounter{}
	return &Metrics{
		OrdersPlaced: n,
		OrdersFailed: n,
	}
}
