package strategy

import (
	"testing"

	"hl-carry-bot/internal/config"
)

func TestFundingNotionalUSDUsesOracle(t *testing.T) {
	snap := MarketSnapshot{
		OraclePrice:  100,
		PerpPosition: -2,
	}
	if got := fundingNotionalUSD(snap); got != 200 {
		t.Fatalf("expected 200, got %f", got)
	}
}

func TestFundingNotionalUSDFallbacks(t *testing.T) {
	snap := MarketSnapshot{
		PerpMidPrice: 50,
		SpotBalance:  3,
	}
	if got := fundingNotionalUSD(snap); got != 150 {
		t.Fatalf("expected 150, got %f", got)
	}

	snap = MarketSnapshot{NotionalUSD: 123}
	if got := fundingNotionalUSD(snap); got != 123 {
		t.Fatalf("expected 123, got %f", got)
	}
}

func TestFundingPaymentEstimateUSD(t *testing.T) {
	snap := MarketSnapshot{
		OraclePrice:  100,
		PerpPosition: 1,
		FundingRate:  0.001,
	}
	if got := FundingPaymentEstimateUSD(snap); got != 0.1 {
		t.Fatalf("expected 0.1, got %f", got)
	}
}

func TestCheckRiskUsesFundingNotional(t *testing.T) {
	cfg := config.RiskConfig{MaxNotionalUSD: 150}
	snap := MarketSnapshot{
		OraclePrice:  100,
		PerpPosition: 2,
	}
	if err := CheckRisk(cfg, snap); err == nil {
		t.Fatalf("expected risk error for notional")
	}
}
