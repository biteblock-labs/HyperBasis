package strategy

const roundTripLegs = 4

func EstimatedCostsUSD(snap MarketSnapshot, feeBps, slippageBps float64) float64 {
	notional := fundingNotionalUSD(snap)
	if notional == 0 {
		notional = snap.NotionalUSD
	}
	if notional == 0 {
		return 0
	}
	rate := (feeBps + slippageBps) / 10000
	if rate <= 0 {
		return 0
	}
	return notional * rate * roundTripLegs
}

func NetExpectedCarryUSD(snap MarketSnapshot, feeBps, slippageBps float64) (float64, float64) {
	cost := EstimatedCostsUSD(snap, feeBps, slippageBps)
	return FundingPaymentEstimateUSD(snap) - cost, cost
}
