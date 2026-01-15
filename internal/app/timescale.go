package app

import (
	"time"

	"hl-carry-bot/internal/strategy"
	"hl-carry-bot/internal/timescale"
)

func (a *App) recordTimescale(state strategy.State, snap strategy.MarketSnapshot, spotExposureUSD, perpExposureUSD, deltaUSD float64) {
	if a.timescale == nil {
		return
	}
	now := time.Now().UTC()
	a.timescale.EnqueuePosition(timescale.PositionSnapshot{
		Time:            now,
		State:           string(state),
		SpotAsset:       snap.SpotAsset,
		PerpAsset:       snap.PerpAsset,
		SpotBalance:     snap.SpotBalance,
		PerpPosition:    snap.PerpPosition,
		SpotMid:         snap.SpotMidPrice,
		PerpMid:         snap.PerpMidPrice,
		OraclePrice:     snap.OraclePrice,
		FundingRate:     snap.FundingRate,
		Volatility:      snap.Volatility,
		DeltaUSD:        deltaUSD,
		SpotExposureUSD: spotExposureUSD,
		PerpExposureUSD: perpExposureUSD,
		NotionalUSD:     snap.NotionalUSD,
		MarginRatio:     snap.MarginRatio,
		HealthRatio:     snap.HealthRatio,
		HasMarginRatio:  snap.HasMarginRatio,
		HasHealthRatio:  snap.HasHealthRatio,
		OpenOrders:      snap.OpenOrderCount,
	})
	if a.market == nil {
		return
	}
	candle, ok := a.market.LatestCandle(snap.PerpAsset)
	if !ok {
		return
	}
	if candle.Interval == "" && a.cfg != nil {
		candle.Interval = a.cfg.Strategy.CandleInterval
	}
	if candle.Start.IsZero() {
		candle.Start = now
	}
	a.timescale.EnqueueCandle(timescale.Candle{
		Asset:    candle.Asset,
		Interval: candle.Interval,
		Start:    candle.Start,
		Open:     candle.Open,
		High:     candle.High,
		Low:      candle.Low,
		Close:    candle.Close,
		Volume:   candle.Volume,
	})
}
