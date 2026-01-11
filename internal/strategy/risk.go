package strategy

import (
	"errors"
	"math"

	"hl-carry-bot/internal/config"
)

func CheckRisk(cfg config.RiskConfig, snap MarketSnapshot) error {
	notional := fundingNotionalUSD(snap)
	if notional == 0 {
		notional = snap.NotionalUSD
	}
	if cfg.MaxNotionalUSD > 0 && notional > cfg.MaxNotionalUSD {
		return errors.New("notional exceeds configured maximum")
	}
	if cfg.MaxOpenOrders > 0 && snap.OpenOrderCount > cfg.MaxOpenOrders {
		return errors.New("open orders exceed configured maximum")
	}
	return nil
}

func fundingNotionalUSD(snap MarketSnapshot) float64 {
	price := priceForFunding(snap)
	if price == 0 {
		if snap.NotionalUSD > 0 {
			return snap.NotionalUSD
		}
		return 0
	}
	if snap.PerpPosition != 0 {
		return math.Abs(snap.PerpPosition) * price
	}
	if snap.SpotBalance != 0 {
		return math.Abs(snap.SpotBalance) * price
	}
	if snap.NotionalUSD > 0 {
		return snap.NotionalUSD
	}
	return 0
}

func FundingPaymentEstimateUSD(snap MarketSnapshot) float64 {
	return fundingNotionalUSD(snap) * snap.FundingRate
}

func priceForFunding(snap MarketSnapshot) float64 {
	if snap.OraclePrice > 0 {
		return snap.OraclePrice
	}
	if snap.PerpMidPrice > 0 {
		return snap.PerpMidPrice
	}
	return snap.SpotMidPrice
}
