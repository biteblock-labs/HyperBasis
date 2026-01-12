package strategy

import (
	"testing"
	"time"

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

func TestCheckRiskMarginRatio(t *testing.T) {
	cfg := config.RiskConfig{MinMarginRatio: 0.25}
	snap := MarketSnapshot{MarginRatio: 0.2, HasMarginRatio: true}
	if err := CheckRisk(cfg, snap); err == nil {
		t.Fatalf("expected risk error for margin ratio")
	}
}

func TestCheckRiskHealthRatio(t *testing.T) {
	cfg := config.RiskConfig{MinHealthRatio: 1.5}
	snap := MarketSnapshot{HealthRatio: 1.2, HasHealthRatio: true}
	if err := CheckRisk(cfg, snap); err == nil {
		t.Fatalf("expected risk error for health ratio")
	}
}

func TestCheckRiskSkipsMissingRatios(t *testing.T) {
	cfg := config.RiskConfig{MinMarginRatio: 0.25, MinHealthRatio: 1.5}
	snap := MarketSnapshot{MarginRatio: 0, HealthRatio: 0}
	if err := CheckRisk(cfg, snap); err != nil {
		t.Fatalf("expected no risk error when ratios missing, got %v", err)
	}
}

func TestCheckConnectivity(t *testing.T) {
	cfg := config.RiskConfig{
		MaxMarketAge:  2 * time.Second,
		MaxAccountAge: 5 * time.Second,
	}
	if err := CheckConnectivity(cfg, 3*time.Second, 1*time.Second); err == nil {
		t.Fatalf("expected market staleness error")
	}
	if err := CheckConnectivity(cfg, 1*time.Second, 6*time.Second); err == nil {
		t.Fatalf("expected account staleness error")
	}
	if err := CheckConnectivity(cfg, 1*time.Second, 2*time.Second); err != nil {
		t.Fatalf("expected connectivity ok, got %v", err)
	}
}
