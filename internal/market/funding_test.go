package market

import (
	"testing"
	"time"
)

func TestParseFundingForecastsSliceMaps(t *testing.T) {
	payload := []any{
		map[string]any{"coin": "BTC", "fundingRate": 0.001, "nextFundingTime": 1700000000000},
	}
	out := parseFundingForecasts(payload)
	if len(out) != 1 {
		t.Fatalf("expected 1 forecast, got %d", len(out))
	}
	forecast, ok := out["BTC"]
	if !ok {
		t.Fatalf("expected BTC forecast")
	}
	if !forecast.HasRate || forecast.Rate != 0.001 {
		t.Fatalf("expected rate 0.001, got %f", forecast.Rate)
	}
	if !forecast.HasNext || forecast.NextFunding.Unix() != 1700000000 {
		t.Fatalf("expected next funding unix 1700000000, got %v", forecast.NextFunding)
	}
}

func TestParseFundingForecastsMapKeys(t *testing.T) {
	payload := map[string]any{
		"ETH": map[string]any{"funding": "0.002", "nextFundingTimeMs": "1700000000000"},
	}
	out := parseFundingForecasts(payload)
	forecast, ok := out["ETH"]
	if !ok {
		t.Fatalf("expected ETH forecast")
	}
	if !forecast.HasRate || forecast.Rate != 0.002 {
		t.Fatalf("expected rate 0.002, got %f", forecast.Rate)
	}
	if !forecast.HasNext || forecast.NextFunding.Unix() != 1700000000 {
		t.Fatalf("expected next funding unix 1700000000, got %v", forecast.NextFunding)
	}
}

func TestParseFundingForecastsProviderList(t *testing.T) {
	payload := []any{
		[]any{"BTC", []any{
			[]any{"BinPerp", map[string]any{"fundingRate": "0.002", "nextFundingTime": 1700000000000}},
			[]any{"HlPerp", map[string]any{"fundingRate": "0.001", "nextFundingTime": 1700000000000}},
		}},
	}
	out := parseFundingForecasts(payload)
	forecast, ok := out["BTC"]
	if !ok {
		t.Fatalf("expected BTC forecast")
	}
	if !forecast.HasRate || forecast.Rate != 0.001 {
		t.Fatalf("expected rate 0.001, got %f", forecast.Rate)
	}
	if forecast.Source != "HlPerp" {
		t.Fatalf("expected source HlPerp, got %q", forecast.Source)
	}
	if !forecast.HasNext || forecast.NextFunding.Unix() != 1700000000 {
		t.Fatalf("expected next funding unix 1700000000, got %v", forecast.NextFunding)
	}
}

func TestParseFundingForecastsProviderFallback(t *testing.T) {
	payload := []any{
		[]any{"SOL", []any{
			[]any{"BinPerp", map[string]any{"fundingRate": 0.003, "nextFundingTime": 1700000000}},
		}},
	}
	out := parseFundingForecasts(payload)
	forecast, ok := out["SOL"]
	if !ok {
		t.Fatalf("expected SOL forecast")
	}
	if !forecast.HasRate || forecast.Rate != 0.003 {
		t.Fatalf("expected rate 0.003, got %f", forecast.Rate)
	}
	if forecast.Source != "BinPerp" {
		t.Fatalf("expected source BinPerp, got %q", forecast.Source)
	}
	if !forecast.HasNext || forecast.NextFunding.Unix() != 1700000000 {
		t.Fatalf("expected next funding unix 1700000000, got %v", forecast.NextFunding)
	}
}

func TestFundingForecastRefreshThrottleUsesAttempts(t *testing.T) {
	md := &MarketData{fundingWindow: 2 * time.Second}
	if !md.shouldRefreshFundingForecast() {
		t.Fatalf("expected refresh when no attempts recorded")
	}
	now := time.Now().UTC()
	md.lastFundingAttempt = now
	if md.shouldRefreshFundingForecast() {
		t.Fatalf("expected refresh throttled within window")
	}
	md.lastFundingAttempt = now.Add(-3 * time.Second)
	if !md.shouldRefreshFundingForecast() {
		t.Fatalf("expected refresh after window elapses")
	}
}
