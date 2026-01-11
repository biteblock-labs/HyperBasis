package strategy

type State string

type Event string

const (
	StateIdle    State = "IDLE"
	StateEnter   State = "ENTER"
	StateHedgeOK State = "HEDGE_OK"
	StateExit    State = "EXIT"
)

const (
	EventEnter   Event = "ENTER"
	EventHedgeOK Event = "HEDGE_OK"
	EventExit    Event = "EXIT"
	EventDone    Event = "DONE"
)

type MarketSnapshot struct {
	PerpAsset      string
	SpotAsset      string
	SpotMidPrice   float64
	PerpMidPrice   float64
	OraclePrice    float64
	FundingRate    float64
	Volatility     float64
	NotionalUSD    float64
	SpotBalance    float64
	PerpPosition   float64
	OpenOrderCount int
}
