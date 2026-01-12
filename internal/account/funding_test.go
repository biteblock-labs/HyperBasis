package account

import (
	"testing"
	"time"
)

func TestParseUserFundingMap(t *testing.T) {
	timeMs := int64(1700000000000)
	payload := map[string]any{
		"userFunding": []any{
			map[string]any{
				"coin":        "BTC",
				"funding":     "0.12",
				"fundingRate": "0.00025",
				"time":        timeMs,
			},
		},
	}

	entries := parseUserFunding(payload)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Asset != "BTC" {
		t.Fatalf("expected asset BTC, got %s", entry.Asset)
	}
	if !entry.HasAmount || entry.Amount != 0.12 {
		t.Fatalf("expected amount 0.12, got %f", entry.Amount)
	}
	if !entry.HasRate || entry.Rate != 0.00025 {
		t.Fatalf("expected rate 0.00025, got %f", entry.Rate)
	}
	wantTime := time.UnixMilli(timeMs).UTC()
	if !entry.HasTime || !entry.Time.Equal(wantTime) {
		t.Fatalf("expected time %s, got %s", wantTime, entry.Time)
	}
	if entry.RawMetadata == nil {
		t.Fatalf("expected raw metadata to be set")
	}
}

func TestParseUserFundingSlice(t *testing.T) {
	timeSec := float64(1700000000)
	payload := []any{
		[]any{"ETH", -0.25, timeSec, 0.0003},
	}

	entries := parseUserFunding(payload)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Asset != "ETH" {
		t.Fatalf("expected asset ETH, got %s", entry.Asset)
	}
	if !entry.HasAmount || entry.Amount != -0.25 {
		t.Fatalf("expected amount -0.25, got %f", entry.Amount)
	}
	wantTime := time.Unix(int64(timeSec), 0).UTC()
	if !entry.HasTime || !entry.Time.Equal(wantTime) {
		t.Fatalf("expected time %s, got %s", wantTime, entry.Time)
	}
	if !entry.HasRate || entry.Rate != 0.0003 {
		t.Fatalf("expected rate 0.0003, got %f", entry.Rate)
	}
}

func TestParseUserFundingDeltaPayload(t *testing.T) {
	timeMs := int64(1767654000068)
	payload := []any{
		map[string]any{
			"time": timeMs,
			"hash": "0xa166e3fa63c25663024b03f2e0da011a00307e4017465df020210d3d432e7cb9",
			"delta": map[string]any{
				"type":        "funding",
				"coin":        "BTC",
				"usdc":        "2.378343",
				"szi":         "-15.0",
				"fundingRate": "0.00000625",
				"nSamples":    nil,
			},
		},
	}

	entries := parseUserFunding(payload)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry.Asset != "BTC" {
		t.Fatalf("expected asset BTC, got %s", entry.Asset)
	}
	if !entry.HasAmount || entry.Amount != 2.378343 {
		t.Fatalf("expected amount 2.378343, got %f", entry.Amount)
	}
	if !entry.HasRate || entry.Rate != 0.00000625 {
		t.Fatalf("expected rate 0.00000625, got %f", entry.Rate)
	}
	wantTime := time.UnixMilli(timeMs).UTC()
	if !entry.HasTime || !entry.Time.Equal(wantTime) {
		t.Fatalf("expected time %s, got %s", wantTime, entry.Time)
	}
	if entry.RawMetadata == nil {
		t.Fatalf("expected raw metadata to be set")
	}
}

func TestParseUserFundingMissingAsset(t *testing.T) {
	payload := []any{
		map[string]any{"funding": "0.2", "time": 1700000000000},
	}
	entries := parseUserFunding(payload)
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}
}
