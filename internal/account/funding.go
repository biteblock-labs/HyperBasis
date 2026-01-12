package account

import (
	"context"
	"errors"
	"strings"
	"time"
)

type FundingPayment struct {
	Asset       string
	Amount      float64
	Rate        float64
	Time        time.Time
	HasAmount   bool
	HasRate     bool
	HasTime     bool
	RawMetadata map[string]any
}

func (a *Account) UserFunding(ctx context.Context, startTimeMs int64) ([]FundingPayment, error) {
	if a.rest == nil {
		return nil, errors.New("rest client is required")
	}
	if a.user == "" {
		return nil, errors.New("account user is required")
	}
	req := map[string]any{
		"type": "userFunding",
		"user": a.user,
	}
	if startTimeMs >= 0 {
		req["startTime"] = startTimeMs
	}
	payload, err := a.rest.InfoAny(ctx, req)
	if err != nil {
		return nil, err
	}
	return parseUserFunding(payload), nil
}

func parseUserFunding(payload any) []FundingPayment {
	if payload == nil {
		return nil
	}
	switch data := payload.(type) {
	case map[string]any:
		if nested, ok := data["data"]; ok {
			return parseUserFunding(nested)
		}
		if nested, ok := data["fundings"]; ok {
			return parseUserFunding(nested)
		}
		if nested, ok := data["userFunding"]; ok {
			return parseUserFunding(nested)
		}
		if entry, ok := parseFundingEntry(data); ok {
			return []FundingPayment{entry}
		}
		return nil
	case []any:
		out := make([]FundingPayment, 0, len(data))
		for _, item := range data {
			entry, ok := parseFundingEntry(item)
			if !ok {
				continue
			}
			out = append(out, entry)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func parseFundingEntry(item any) (FundingPayment, bool) {
	switch data := item.(type) {
	case map[string]any:
		if deltaRaw, ok := data["delta"]; ok {
			if delta, ok := deltaRaw.(map[string]any); ok {
				entry, ok := parseFundingDelta(delta)
				if !ok {
					return FundingPayment{}, false
				}
				entry.RawMetadata = data
				if ts, ok := timeFromMap(data, "time", "timestamp", "fundingTime", "ts"); ok {
					entry.Time = ts
					entry.HasTime = true
				} else if !entry.HasTime {
					if ts, ok := timeFromMap(delta, "time", "timestamp", "fundingTime", "ts"); ok {
						entry.Time = ts
						entry.HasTime = true
					}
				}
				if !entry.HasAmount && !entry.HasRate && !entry.HasTime {
					return FundingPayment{}, false
				}
				return entry, true
			}
		}
		asset := stringFromMap(data, "coin", "asset", "symbol")
		if asset == "" {
			return FundingPayment{}, false
		}
		entry := FundingPayment{
			Asset:       asset,
			RawMetadata: data,
		}
		if amt, ok := floatFromMap(data, "funding", "fundingPayment", "amount", "usdc"); ok {
			entry.Amount = amt
			entry.HasAmount = true
		}
		if rate, ok := floatFromMap(data, "fundingRate", "rate"); ok {
			entry.Rate = rate
			entry.HasRate = true
		}
		if ts, ok := timeFromMap(data, "time", "timestamp", "fundingTime", "ts"); ok {
			entry.Time = ts
			entry.HasTime = true
		}
		if !entry.HasAmount && !entry.HasRate && !entry.HasTime {
			return FundingPayment{}, false
		}
		return entry, true
	case []any:
		return parseFundingSlice(data)
	default:
		return FundingPayment{}, false
	}
}

func parseFundingDelta(delta map[string]any) (FundingPayment, bool) {
	deltaType := strings.ToLower(stringFromMap(delta, "type"))
	if deltaType != "" && deltaType != "funding" {
		return FundingPayment{}, false
	}
	asset := stringFromMap(delta, "coin", "asset", "symbol")
	if asset == "" {
		return FundingPayment{}, false
	}
	entry := FundingPayment{
		Asset:       asset,
		RawMetadata: delta,
	}
	if amt, ok := floatFromMap(delta, "usdc", "funding", "fundingPayment", "amount"); ok {
		entry.Amount = amt
		entry.HasAmount = true
	}
	if rate, ok := floatFromMap(delta, "fundingRate", "rate"); ok {
		entry.Rate = rate
		entry.HasRate = true
	}
	if ts, ok := timeFromMap(delta, "time", "timestamp", "fundingTime", "ts"); ok {
		entry.Time = ts
		entry.HasTime = true
	}
	if !entry.HasAmount && !entry.HasRate && !entry.HasTime {
		return FundingPayment{}, false
	}
	return entry, true
}

func parseFundingSlice(data []any) (FundingPayment, bool) {
	if len(data) < 2 {
		return FundingPayment{}, false
	}
	asset := stringFromAny(data[0])
	if asset == "" && len(data) >= 2 {
		asset = stringFromAny(data[1])
	}
	if asset == "" {
		return FundingPayment{}, false
	}
	entry := FundingPayment{Asset: asset}
	for i, item := range data {
		if i == 0 || i == 1 {
			continue
		}
		if entry.HasTime {
			break
		}
		if ts, ok := timeFromAny(item); ok {
			entry.Time = ts
			entry.HasTime = true
			break
		}
	}
	if amt, ok := floatFromAny(data[1]); ok {
		entry.Amount = amt
		entry.HasAmount = true
	}
	if len(data) >= 3 && !entry.HasTime {
		if ts, ok := timeFromAny(data[2]); ok {
			entry.Time = ts
			entry.HasTime = true
		}
	}
	if len(data) >= 4 && !entry.HasRate {
		if rate, ok := floatFromAny(data[3]); ok {
			entry.Rate = rate
			entry.HasRate = true
		}
	}
	if !entry.HasAmount && !entry.HasRate && !entry.HasTime {
		return FundingPayment{}, false
	}
	return entry, true
}

func stringFromMap(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s := stringFromAny(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func floatFromMap(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if f, ok := floatFromAny(v); ok {
				return f, true
			}
		}
	}
	return 0, false
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
