package market

import (
	"context"
	"errors"
	"strings"
	"time"

	"hl-carry-bot/internal/hl/rest"
)

type FundingForecast struct {
	Rate         float64
	NextFunding  time.Time
	ObservedAt   time.Time
	Interval     time.Duration
	HasNext      bool
	HasRate      bool
	RawAssetName string
	Source       string
}

func (m *MarketData) RefreshFundingForecast(ctx context.Context) (bool, error) {
	if m.rest == nil {
		return false, nil
	}
	if !m.shouldRefreshFundingForecast() {
		return false, nil
	}
	now := time.Now().UTC()
	m.mu.Lock()
	m.lastFundingAttempt = now
	m.mu.Unlock()
	payload, err := m.rest.InfoAny(ctx, rest.InfoRequest{Type: "predictedFundings"})
	if err != nil {
		return false, err
	}
	forecasts := parseFundingForecasts(payload)
	if len(forecasts) == 0 {
		return false, errors.New("predicted fundings missing")
	}
	now = time.Now().UTC()
	for key, forecast := range forecasts {
		forecast.ObservedAt = now
		forecast = normalizeFundingForecast(forecast, now)
		forecasts[key] = forecast
	}
	m.mu.Lock()
	m.fundingForecasts = forecasts
	m.lastFundingFetch = now
	m.mu.Unlock()
	return true, nil
}

func (m *MarketData) FundingForecast(asset string) (FundingForecast, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	forecast, ok := m.fundingForecasts[asset]
	return forecast, ok
}

func (m *MarketData) shouldRefreshFundingForecast() bool {
	m.mu.RLock()
	last := m.lastFundingAttempt
	window := m.fundingWindow
	m.mu.RUnlock()
	if window <= 0 {
		return true
	}
	if last.IsZero() {
		return true
	}
	return time.Since(last) >= window
}

func parseFundingForecasts(payload any) map[string]FundingForecast {
	if payload == nil {
		return nil
	}
	switch data := payload.(type) {
	case map[string]any:
		if nested, ok := data["predictedFundings"]; ok {
			return parseFundingForecasts(nested)
		}
		if nested, ok := data["data"]; ok {
			return parseFundingForecasts(nested)
		}
		if forecast, ok := parseFundingForecastItem("", data); ok {
			return map[string]FundingForecast{forecast.RawAssetName: forecast}
		}
		out := make(map[string]FundingForecast)
		for key, val := range data {
			if key == "" {
				continue
			}
			forecast, ok := parseFundingForecastItem(key, val)
			if !ok {
				continue
			}
			out[forecast.RawAssetName] = forecast
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make(map[string]FundingForecast)
		for _, item := range data {
			if forecast, ok := parseFundingForecastProviders(item); ok {
				out[forecast.RawAssetName] = forecast
				continue
			}
			forecast, ok := parseFundingForecastItem("", item)
			if !ok {
				continue
			}
			out[forecast.RawAssetName] = forecast
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func parseFundingForecastItem(asset string, payload any) (FundingForecast, bool) {
	switch data := payload.(type) {
	case map[string]any:
		name := asset
		if name == "" {
			name = stringFromMap(data, "coin", "asset", "symbol")
		}
		if name == "" {
			return FundingForecast{}, false
		}
		forecast := FundingForecast{RawAssetName: name}
		if rate := floatFromMap(data, "fundingRate", "funding", "rate", "predictedFunding"); rate != 0 {
			forecast.Rate = rate
			forecast.HasRate = true
		}
		if ts, ok := timeFromMap(data, "nextFundingTime", "nextFundingTimeMs", "fundingTime", "nextFunding", "time", "timestamp"); ok {
			forecast.NextFunding = ts
			forecast.HasNext = true
		}
		if interval := floatFromMap(data, "fundingIntervalHours", "fundingIntervalHrs", "intervalHours", "intervalHrs"); interval > 0 {
			forecast.Interval = time.Duration(interval * float64(time.Hour))
		}
		if !forecast.HasRate && !forecast.HasNext {
			return FundingForecast{}, false
		}
		return forecast, true
	case []any:
		if len(data) < 2 {
			return FundingForecast{}, false
		}
		name := asset
		if name == "" {
			name = stringFromAny(data[0])
		}
		if name == "" {
			return FundingForecast{}, false
		}
		forecast := FundingForecast{RawAssetName: name}
		if rate, ok := floatFromAny(data[1]); ok {
			forecast.Rate = rate
			forecast.HasRate = true
		}
		if len(data) >= 3 {
			if ts, ok := timeFromAny(data[2]); ok {
				forecast.NextFunding = ts
				forecast.HasNext = true
			}
		}
		if !forecast.HasRate && !forecast.HasNext {
			return FundingForecast{}, false
		}
		return forecast, true
	default:
		return FundingForecast{}, false
	}
}

func parseFundingForecastProviders(payload any) (FundingForecast, bool) {
	entry, ok := payload.([]any)
	if !ok || len(entry) < 2 {
		return FundingForecast{}, false
	}
	asset := stringFromAny(entry[0])
	if asset == "" {
		return FundingForecast{}, false
	}
	providers, ok := entry[1].([]any)
	if !ok || len(providers) == 0 {
		return FundingForecast{}, false
	}
	return forecastFromProviders(asset, providers)
}

func forecastFromProviders(asset string, providers []any) (FundingForecast, bool) {
	var fallback *FundingForecast
	for _, provider := range providers {
		pair, ok := provider.([]any)
		if !ok || len(pair) < 2 {
			continue
		}
		source := stringFromAny(pair[0])
		forecast, ok := parseProviderForecast(asset, source, pair[1])
		if !ok {
			continue
		}
		if source != "" && strings.EqualFold(source, "HlPerp") {
			return forecast, true
		}
		if fallback == nil {
			copy := forecast
			fallback = &copy
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return FundingForecast{}, false
}

func parseProviderForecast(asset, source string, payload any) (FundingForecast, bool) {
	data, ok := payload.(map[string]any)
	if !ok {
		return FundingForecast{}, false
	}
	if asset == "" {
		return FundingForecast{}, false
	}
	forecast := FundingForecast{
		RawAssetName: asset,
		Source:       source,
	}
	if rate := floatFromMap(data, "fundingRate", "funding", "rate", "predictedFunding"); rate != 0 {
		forecast.Rate = rate
		forecast.HasRate = true
	}
	if ts, ok := timeFromMap(data, "nextFundingTime", "nextFundingTimeMs", "fundingTime", "nextFunding", "time", "timestamp"); ok {
		forecast.NextFunding = ts
		forecast.HasNext = true
	}
	if interval := floatFromMap(data, "fundingIntervalHours", "fundingIntervalHrs", "intervalHours", "intervalHrs"); interval > 0 {
		forecast.Interval = time.Duration(interval * float64(time.Hour))
	}
	if !forecast.HasRate && !forecast.HasNext {
		return FundingForecast{}, false
	}
	return forecast, true
}

func normalizeFundingForecast(forecast FundingForecast, now time.Time) FundingForecast {
	if !forecast.HasNext || forecast.NextFunding.IsZero() {
		return forecast
	}
	if forecast.NextFunding.After(now) {
		return forecast
	}
	if forecast.Interval <= 0 {
		return forecast
	}
	steps := now.Sub(forecast.NextFunding)/forecast.Interval + 1
	if steps > 0 {
		forecast.NextFunding = forecast.NextFunding.Add(time.Duration(steps) * forecast.Interval)
	}
	return forecast
}

func timeFromMap(m map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if ts, ok := timeFromAny(v); ok {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func timeFromAny(v any) (time.Time, bool) {
	f, ok := floatFromAny(v)
	if !ok {
		return time.Time{}, false
	}
	if f <= 0 {
		return time.Time{}, false
	}
	ts := int64(f)
	switch {
	case ts > 1e15:
		return time.Unix(0, ts).UTC(), true
	case ts > 1e12:
		return time.UnixMilli(ts).UTC(), true
	default:
		return time.Unix(ts, 0).UTC(), true
	}
}
