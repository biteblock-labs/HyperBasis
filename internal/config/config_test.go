package config

import "testing"

func TestStrategyAssetDefaults(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{Asset: "BTC", NotionalUSD: 1}}
	applyDefaults(cfg)
	if cfg.Strategy.PerpAsset != "BTC" {
		t.Fatalf("expected perp asset BTC, got %q", cfg.Strategy.PerpAsset)
	}
	if cfg.Strategy.SpotAsset != "BTC" {
		t.Fatalf("expected spot asset BTC, got %q", cfg.Strategy.SpotAsset)
	}
}

func TestStrategyAssetDefaultsFromPerp(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{PerpAsset: "ETH", NotionalUSD: 1}}
	applyDefaults(cfg)
	if cfg.Strategy.SpotAsset != "ETH" {
		t.Fatalf("expected spot asset ETH, got %q", cfg.Strategy.SpotAsset)
	}
}

func TestStrategyEntryDefaults(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{PerpAsset: "BTC", SpotAsset: "UBTC", NotionalUSD: 1}}
	applyDefaults(cfg)
	if cfg.Strategy.EntryTimeout <= 0 {
		t.Fatalf("expected entry timeout default, got %v", cfg.Strategy.EntryTimeout)
	}
	if cfg.Strategy.EntryPollInterval <= 0 {
		t.Fatalf("expected entry poll interval default, got %v", cfg.Strategy.EntryPollInterval)
	}
	if cfg.Strategy.MinExposureUSD <= 0 {
		t.Fatalf("expected min exposure default, got %v", cfg.Strategy.MinExposureUSD)
	}
}

func TestValidateRequiresPerpAsset(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{NotionalUSD: 1}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for missing perp asset")
	}
}
