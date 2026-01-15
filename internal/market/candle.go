package market

import "time"

type Candle struct {
	Asset    string
	Interval string
	Start    time.Time
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}
