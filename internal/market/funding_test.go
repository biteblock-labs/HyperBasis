package market

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hl-carry-bot/internal/hl/rest"

	"go.uber.org/zap"
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
			[]any{"HlPerp", map[string]any{"fundingRate": "0.001", "nextFundingTime": 1700000000000, "fundingIntervalHours": 1}},
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
	if forecast.Interval != time.Hour {
		t.Fatalf("expected interval 1h, got %s", forecast.Interval)
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

func TestRefreshFundingForecastSetsObservedAt(t *testing.T) {
	payload := `[[ "BTC", [["HlPerp", {"fundingRate":"0.001","nextFundingTime":1700000000000}]] ]]`
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	md := New(rest.New(srv.URL, 2*time.Second, zap.NewNop()), nil, zap.NewNop())
	md.fundingWindow = 0

	ok, err := md.RefreshFundingForecast(context.Background())
	if err != nil {
		t.Fatalf("refresh error: %v", err)
	}
	if !ok {
		t.Fatalf("expected refresh to run")
	}
	forecast, ok := md.FundingForecast("BTC")
	if !ok {
		t.Fatalf("expected BTC forecast")
	}
	if forecast.ObservedAt.IsZero() {
		t.Fatalf("expected observed_at to be set")
	}
	if time.Since(forecast.ObservedAt) > 2*time.Second {
		t.Fatalf("observed_at too old: %s", time.Since(forecast.ObservedAt))
	}
}

func TestRefreshFundingForecastRollsForwardNextFunding(t *testing.T) {
	past := time.Now().Add(-30 * time.Minute).UnixMilli()
	payload := fmt.Sprintf(`[[ "BTC", [["HlPerp", {"fundingRate":"0.001","nextFundingTime":%d,"fundingIntervalHours":1}]] ]]`, past)
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	md := New(rest.New(srv.URL, 2*time.Second, zap.NewNop()), nil, zap.NewNop())
	md.fundingWindow = 0

	ok, err := md.RefreshFundingForecast(context.Background())
	if err != nil {
		t.Fatalf("refresh error: %v", err)
	}
	if !ok {
		t.Fatalf("expected refresh to run")
	}
	forecast, ok := md.FundingForecast("BTC")
	if !ok {
		t.Fatalf("expected BTC forecast")
	}
	if forecast.Interval != time.Hour {
		t.Fatalf("expected interval 1h, got %s", forecast.Interval)
	}
	if !forecast.NextFunding.After(forecast.ObservedAt) {
		t.Fatalf("expected next funding after observed_at, got %s vs %s", forecast.NextFunding, forecast.ObservedAt)
	}
}
