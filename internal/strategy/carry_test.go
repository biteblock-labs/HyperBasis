package strategy

import "testing"

func TestEstimatedCostsUSDUsesNotional(t *testing.T) {
	snap := MarketSnapshot{NotionalUSD: 1000}
	cost := EstimatedCostsUSD(snap, 10, 5)
	if cost != 6 {
		t.Fatalf("expected cost 6, got %f", cost)
	}
}

func TestEstimatedCostsUSDUsesPerpPosition(t *testing.T) {
	snap := MarketSnapshot{
		OraclePrice:  100,
		PerpPosition: -2,
	}
	cost := EstimatedCostsUSD(snap, 10, 0)
	if cost != 0.8 {
		t.Fatalf("expected cost 0.8, got %f", cost)
	}
}

func TestNetExpectedCarryUSD(t *testing.T) {
	snap := MarketSnapshot{
		OraclePrice:  100,
		PerpPosition: 1,
		FundingRate:  0.01,
	}
	net, cost := NetExpectedCarryUSD(snap, 10, 0)
	if cost != 0.4 {
		t.Fatalf("expected cost 0.4, got %f", cost)
	}
	if net != 0.6 {
		t.Fatalf("expected net 0.6, got %f", net)
	}
}
