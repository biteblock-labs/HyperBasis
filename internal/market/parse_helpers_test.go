package market

import "testing"

func TestParsePerpContextsArray(t *testing.T) {
	payload := []any{
		map[string]any{
			"universe": []any{
				map[string]any{"name": "BTC", "szDecimals": 5},
				map[string]any{"name": "ETH", "szDecimals": 4},
			},
		},
		[]any{
			map[string]any{"funding": "0.001", "oraclePx": "30000", "markPx": "30010"},
			map[string]any{"fundingRate": 0.002, "oraclePrice": 2000.0, "markPrice": 1995.0},
		},
	}

	ctxs, err := parsePerpContexts(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	btc := ctxs["BTC"]
	if !closeEnough(btc.FundingRate, 0.001) {
		t.Fatalf("expected BTC funding 0.001, got %f", btc.FundingRate)
	}
	if !closeEnough(btc.OraclePrice, 30000) {
		t.Fatalf("expected BTC oracle 30000, got %f", btc.OraclePrice)
	}
	if btc.Index != 0 {
		t.Fatalf("expected BTC index 0, got %d", btc.Index)
	}
	if btc.SzDecimals != 5 {
		t.Fatalf("expected BTC sz decimals 5, got %d", btc.SzDecimals)
	}
	eth := ctxs["ETH"]
	if !closeEnough(eth.FundingRate, 0.002) {
		t.Fatalf("expected ETH funding 0.002, got %f", eth.FundingRate)
	}
}

func TestParsePerpContextsMap(t *testing.T) {
	payload := map[string]any{
		"universe": []any{
			map[string]any{"name": "SOL"},
		},
		"assetCtxs": []any{
			map[string]any{"funding": 0.005, "oraclePx": 20.5},
		},
	}

	ctxs, err := parsePerpContexts(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !closeEnough(ctxs["SOL"].FundingRate, 0.005) {
		t.Fatalf("expected SOL funding 0.005, got %f", ctxs["SOL"].FundingRate)
	}
}

func TestParseSpotContexts(t *testing.T) {
	payload := []any{
		map[string]any{
			"universe": []any{
				map[string]any{"name": "@0", "index": 0, "tokens": []any{1, 0}},
				map[string]any{"name": "ETH/USDC", "index": 1, "tokens": []any{2, 0}},
			},
			"tokens": []any{
				map[string]any{"name": "USDC", "index": 0, "szDecimals": 8},
				map[string]any{"name": "BTC", "index": 1, "szDecimals": 5},
				map[string]any{"name": "ETH", "index": 2, "szDecimals": 4},
			},
		},
		[]any{},
	}

	ctxs, err := parseSpotContexts(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	btc := ctxs["BTC/USDC"]
	if btc.Index != 0 {
		t.Fatalf("expected BTC/USDC index 0, got %d", ctxs["BTC/USDC"].Index)
	}
	if btc.MidKey != "@0" {
		t.Fatalf("expected BTC/USDC mid key @0, got %s", btc.MidKey)
	}
	if btc.BaseSzDecimals != 5 {
		t.Fatalf("expected BTC sz decimals 5, got %d", btc.BaseSzDecimals)
	}
	if ctxs["ETH/USDC"].Symbol == "" {
		t.Fatalf("expected ETH/USDC symbol to be parsed")
	}
}

func TestParseCandle(t *testing.T) {
	payload := map[string]any{
		"data": map[string]any{
			"coin": "BTC",
			"candle": map[string]any{
				"close": "100.5",
			},
		},
	}
	asset, close, ok := parseCandle(payload)
	if !ok {
		t.Fatalf("expected candle parsed")
	}
	if asset != "BTC" {
		t.Fatalf("expected asset BTC, got %s", asset)
	}
	if !closeEnough(close, 100.5) {
		t.Fatalf("expected close 100.5, got %f", close)
	}
}

func TestParseCandleOHLC(t *testing.T) {
	payload := map[string]any{
		"data": map[string]any{
			"coin":     "ETH",
			"interval": "1h",
			"candle": map[string]any{
				"t":      1710000000000,
				"open":   "100.0",
				"high":   "110.0",
				"low":    "90.0",
				"close":  "105.0",
				"volume": "12.5",
			},
		},
	}
	candle, ok := parseCandleOHLC(payload)
	if !ok {
		t.Fatalf("expected candle parsed")
	}
	if candle.Asset != "ETH" {
		t.Fatalf("expected asset ETH, got %s", candle.Asset)
	}
	if candle.Interval != "1h" {
		t.Fatalf("expected interval 1h, got %s", candle.Interval)
	}
	if !closeEnough(candle.Open, 100.0) || !closeEnough(candle.High, 110.0) || !closeEnough(candle.Low, 90.0) || !closeEnough(candle.Close, 105.0) {
		t.Fatalf("unexpected ohlc values")
	}
	if !closeEnough(candle.Volume, 12.5) {
		t.Fatalf("expected volume 12.5, got %f", candle.Volume)
	}
	if candle.Start.IsZero() {
		t.Fatalf("expected candle start time")
	}
}

func TestParseCandleWS(t *testing.T) {
	payload := map[string]any{
		"channel": "candle",
		"data": map[string]any{
			"t": 1768334400000,
			"T": 1768337999999,
			"s": "ETH",
			"i": "1h",
			"o": "3215.8",
			"c": "3215.7",
			"h": "3219.3",
			"l": "3208.5",
			"v": "2710.7687",
			"n": 2134,
		},
	}
	asset, close, ok := parseCandle(payload)
	if !ok {
		t.Fatalf("expected candle parsed")
	}
	if asset != "ETH" {
		t.Fatalf("expected asset ETH, got %s", asset)
	}
	if !closeEnough(close, 3215.7) {
		t.Fatalf("expected close 3215.7, got %f", close)
	}
}

func TestParseCandleOHLCWS(t *testing.T) {
	payload := map[string]any{
		"channel": "candle",
		"data": map[string]any{
			"t": 1768334400000,
			"T": 1768337999999,
			"s": "ETH",
			"i": "1h",
			"o": "3215.8",
			"c": "3215.7",
			"h": "3219.3",
			"l": "3208.5",
			"v": "2710.7687",
			"n": 2134,
		},
	}
	candle, ok := parseCandleOHLC(payload)
	if !ok {
		t.Fatalf("expected candle parsed")
	}
	if candle.Asset != "ETH" {
		t.Fatalf("expected asset ETH, got %s", candle.Asset)
	}
	if candle.Interval != "1h" {
		t.Fatalf("expected interval 1h, got %s", candle.Interval)
	}
	if !closeEnough(candle.Open, 3215.8) || !closeEnough(candle.High, 3219.3) || !closeEnough(candle.Low, 3208.5) || !closeEnough(candle.Close, 3215.7) {
		t.Fatalf("unexpected ohlc values")
	}
	if !closeEnough(candle.Volume, 2710.7687) {
		t.Fatalf("expected volume 2710.7687, got %f", candle.Volume)
	}
	if candle.Start.IsZero() {
		t.Fatalf("expected candle start time")
	}
}

func TestComputeVolatility(t *testing.T) {
	flat := computeVolatility([]float64{100, 110, 121})
	if flat != 0 {
		t.Fatalf("expected zero volatility, got %f", flat)
	}
	vol := computeVolatility([]float64{100, 110, 100})
	if vol <= 0 {
		t.Fatalf("expected positive volatility, got %f", vol)
	}
}

func closeEnough(a, b float64) bool {
	const eps = 1e-9
	if a > b {
		return a-b < eps
	}
	return b-a < eps
}
