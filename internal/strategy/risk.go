package strategy

import (
	"errors"
	"fmt"
	"math"
	"time"

	"hl-carry-bot/internal/config"
)

var (
	ErrMarketStale  = errors.New("market data stale")
	ErrAccountStale = errors.New("account data stale")
	ErrMarginRatio  = errors.New("margin ratio below threshold")
	ErrHealthRatio  = errors.New("account health below threshold")
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
	if cfg.MinMarginRatio > 0 && snap.HasMarginRatio && snap.MarginRatio < cfg.MinMarginRatio {
		return fmt.Errorf("margin ratio %.4f below %.4f: %w", snap.MarginRatio, cfg.MinMarginRatio, ErrMarginRatio)
	}
	if cfg.MinHealthRatio > 0 && snap.HasHealthRatio && snap.HealthRatio < cfg.MinHealthRatio {
		return fmt.Errorf("health ratio %.4f below %.4f: %w", snap.HealthRatio, cfg.MinHealthRatio, ErrHealthRatio)
	}
	return nil
}

func CheckConnectivity(cfg config.RiskConfig, marketAge, accountAge time.Duration) error {
	if cfg.MaxMarketAge > 0 && marketAge > cfg.MaxMarketAge {
		return fmt.Errorf("market data age %s exceeds %s: %w", marketAge, cfg.MaxMarketAge, ErrMarketStale)
	}
	if cfg.MaxAccountAge > 0 && accountAge > cfg.MaxAccountAge {
		return fmt.Errorf("account data age %s exceeds %s: %w", accountAge, cfg.MaxAccountAge, ErrAccountStale)
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
