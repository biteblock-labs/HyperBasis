package config

import (
	"testing"
	"time"
)

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
	if cfg.Strategy.EntryCooldown <= 0 {
		t.Fatalf("expected entry cooldown default, got %v", cfg.Strategy.EntryCooldown)
	}
	if cfg.Strategy.HedgeCooldown <= 0 {
		t.Fatalf("expected hedge cooldown default, got %v", cfg.Strategy.HedgeCooldown)
	}
	if cfg.Strategy.ExitFundingGuard <= 0 {
		t.Fatalf("expected exit funding guard default, got %v", cfg.Strategy.ExitFundingGuard)
	}
	if cfg.Strategy.ExitFundingGuardEnabled == nil || !*cfg.Strategy.ExitFundingGuardEnabled {
		t.Fatalf("expected exit funding guard enabled default")
	}
	if cfg.Strategy.MinExposureUSD <= 0 {
		t.Fatalf("expected min exposure default, got %v", cfg.Strategy.MinExposureUSD)
	}
	if cfg.Strategy.DeltaBandUSD <= 0 {
		t.Fatalf("expected delta band default, got %v", cfg.Strategy.DeltaBandUSD)
	}
	if cfg.Strategy.FundingConfirmations <= 0 {
		t.Fatalf("expected funding confirmations default, got %v", cfg.Strategy.FundingConfirmations)
	}
	if cfg.Strategy.FundingDipConfirmations <= 0 {
		t.Fatalf("expected funding dip confirmations default, got %v", cfg.Strategy.FundingDipConfirmations)
	}
	if cfg.Strategy.SpotReconcileInterval <= 0 {
		t.Fatalf("expected spot reconcile interval default, got %v", cfg.Strategy.SpotReconcileInterval)
	}
}

func TestRiskDefaults(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{PerpAsset: "BTC", SpotAsset: "UBTC", NotionalUSD: 1}}
	applyDefaults(cfg)
	if cfg.Risk.MaxMarketAge <= 0 {
		t.Fatalf("expected max market age default, got %v", cfg.Risk.MaxMarketAge)
	}
	if cfg.Risk.MaxAccountAge <= 0 {
		t.Fatalf("expected max account age default, got %v", cfg.Risk.MaxAccountAge)
	}
}

func TestMetricsDefaults(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{PerpAsset: "BTC", SpotAsset: "UBTC", NotionalUSD: 1}}
	applyDefaults(cfg)
	if cfg.Metrics.Enabled == nil || !cfg.Metrics.EnabledValue() {
		t.Fatalf("expected metrics enabled default")
	}
	if cfg.Metrics.Address != "127.0.0.1:9001" {
		t.Fatalf("expected metrics address default, got %q", cfg.Metrics.Address)
	}
	if cfg.Metrics.Path != "/metrics" {
		t.Fatalf("expected metrics path default, got %q", cfg.Metrics.Path)
	}
}

func TestWSURLDerivedFromREST(t *testing.T) {
	cfg := &Config{REST: RESTConfig{BaseURL: "https://example.com"}}
	applyDefaults(cfg)
	if cfg.WS.URL != "wss://example.com/ws" {
		t.Fatalf("expected derived ws url, got %q", cfg.WS.URL)
	}
}

func TestWSURLDerivedFromRESTHTTP(t *testing.T) {
	cfg := &Config{REST: RESTConfig{BaseURL: "http://example.com"}}
	applyDefaults(cfg)
	if cfg.WS.URL != "ws://example.com/ws" {
		t.Fatalf("expected derived ws url, got %q", cfg.WS.URL)
	}
}

func TestWSURLRespectsExplicitValue(t *testing.T) {
	cfg := &Config{
		REST: RESTConfig{BaseURL: "https://example.com"},
		WS:   WSConfig{URL: "wss://override.example/ws"},
	}
	applyDefaults(cfg)
	if cfg.WS.URL != "wss://override.example/ws" {
		t.Fatalf("expected explicit ws url, got %q", cfg.WS.URL)
	}
}

func TestValidateRequiresPerpAsset(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{NotionalUSD: 1}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for missing perp asset")
	}
}

func TestValidateRejectsNegativeSpotReconcileInterval(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{
		PerpAsset:             "BTC",
		SpotAsset:             "UBTC",
		NotionalUSD:           1,
		SpotReconcileInterval: -1 * time.Second,
	}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for negative spot reconcile interval")
	}
}

func TestValidateRejectsNegativeDeltaBand(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{
		PerpAsset:    "BTC",
		SpotAsset:    "UBTC",
		NotionalUSD:  1,
		DeltaBandUSD: -1,
	}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for negative delta band")
	}
}

func TestValidateRejectsNegativeEntryCooldown(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{
		PerpAsset:     "BTC",
		SpotAsset:     "UBTC",
		NotionalUSD:   1,
		EntryCooldown: -1 * time.Second,
	}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for negative entry cooldown")
	}
}

func TestValidateRejectsNegativeHedgeCooldown(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{
		PerpAsset:     "BTC",
		SpotAsset:     "UBTC",
		NotionalUSD:   1,
		HedgeCooldown: -1 * time.Second,
	}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for negative hedge cooldown")
	}
}

func TestValidateRejectsNegativeExitFundingGuard(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{
		PerpAsset:        "BTC",
		SpotAsset:        "UBTC",
		NotionalUSD:      1,
		ExitFundingGuard: -1 * time.Second,
	}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for negative exit funding guard")
	}
}

func TestValidateRejectsMetricsPathWithoutSlash(t *testing.T) {
	cfg := &Config{
		Metrics: MetricsConfig{Path: "metrics"},
		Strategy: StrategyConfig{
			PerpAsset:   "BTC",
			SpotAsset:   "UBTC",
			NotionalUSD: 1,
		},
	}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for metrics path without leading slash")
	}
}

func TestValidateRejectsTelegramEnabledWithoutConfig(t *testing.T) {
	t.Setenv("HL_TELEGRAM_TOKEN", "")
	t.Setenv("HL_TELEGRAM_CHAT_ID", "")
	cfg := &Config{
		Telegram: TelegramConfig{Enabled: true},
		Strategy: StrategyConfig{
			PerpAsset:   "BTC",
			SpotAsset:   "UBTC",
			NotionalUSD: 1,
		},
	}
	applyDefaults(cfg)
	applyEnvOverrides(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for missing telegram token/chat_id")
	}
}

func TestTelegramEnvOverridesConfig(t *testing.T) {
	t.Setenv("HL_TELEGRAM_TOKEN", "env-token")
	t.Setenv("HL_TELEGRAM_CHAT_ID", "123")
	cfg := &Config{
		Telegram: TelegramConfig{
			Enabled: true,
			Token:   "config-token",
			ChatID:  "999",
		},
		Strategy: StrategyConfig{
			PerpAsset:   "BTC",
			SpotAsset:   "UBTC",
			NotionalUSD: 1,
		},
	}
	applyDefaults(cfg)
	applyEnvOverrides(cfg)
	if cfg.Telegram.Token != "env-token" {
		t.Fatalf("expected env token override, got %q", cfg.Telegram.Token)
	}
	if cfg.Telegram.ChatID != "123" {
		t.Fatalf("expected env chat id override, got %q", cfg.Telegram.ChatID)
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("expected valid config with env overrides, got %v", err)
	}
}

func TestExitFundingGuardEnabledFalseRespected(t *testing.T) {
	enabled := false
	cfg := &Config{Strategy: StrategyConfig{
		PerpAsset:               "BTC",
		SpotAsset:               "UBTC",
		NotionalUSD:             1,
		ExitFundingGuardEnabled: &enabled,
	}}
	applyDefaults(cfg)
	if cfg.Strategy.ExitFundingGuardEnabled == nil || *cfg.Strategy.ExitFundingGuardEnabled {
		t.Fatalf("expected exit funding guard enabled=false to be preserved")
	}
}

func TestValidateRejectsNegativeCarrySettings(t *testing.T) {
	cfg := &Config{Strategy: StrategyConfig{
		PerpAsset:               "BTC",
		SpotAsset:               "UBTC",
		NotionalUSD:             1,
		FeeBps:                  -1,
		SlippageBps:             -1,
		IOCPriceBps:             -1,
		CarryBufferUSD:          -1,
		FundingConfirmations:    -1,
		FundingDipConfirmations: -1,
	}}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for negative carry settings")
	}
}

func TestValidateRejectsNegativeRiskAges(t *testing.T) {
	cfg := &Config{
		Strategy: StrategyConfig{PerpAsset: "BTC", SpotAsset: "UBTC", NotionalUSD: 1},
		Risk: RiskConfig{
			MaxMarketAge:  -1 * time.Second,
			MaxAccountAge: -1 * time.Second,
		},
	}
	applyDefaults(cfg)
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for negative risk ages")
	}
}
