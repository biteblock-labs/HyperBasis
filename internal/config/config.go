package config

import (
	"errors"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Log      LoggingConfig  `yaml:"log"`
	REST     RESTConfig     `yaml:"rest"`
	WS       WSConfig       `yaml:"ws"`
	State    StateConfig    `yaml:"state"`
	Strategy StrategyConfig `yaml:"strategy"`
	Risk     RiskConfig     `yaml:"risk"`
	Telegram TelegramConfig `yaml:"telegram"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

type RESTConfig struct {
	BaseURL string        `yaml:"base_url"`
	Timeout time.Duration `yaml:"timeout"`
}

type WSConfig struct {
	URL            string        `yaml:"url"`
	ReconnectDelay time.Duration `yaml:"reconnect_delay"`
}

type StateConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

type StrategyConfig struct {
	Asset            string        `yaml:"asset"`
	NotionalUSD      float64       `yaml:"notional_usd"`
	MinFundingRate   float64       `yaml:"min_funding_rate"`
	MaxVolatility    float64       `yaml:"max_volatility"`
	EntryInterval    time.Duration `yaml:"entry_interval"`
	ExitOnFundingDip bool          `yaml:"exit_on_funding_dip"`
	CandleInterval   string        `yaml:"candle_interval"`
	CandleWindow     int           `yaml:"candle_window"`
}

type RiskConfig struct {
	MaxNotionalUSD float64 `yaml:"max_notional_usd"`
	MaxOpenOrders  int     `yaml:"max_open_orders"`
}

type TelegramConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
	ChatID  string `yaml:"chat_id"`
}

func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	applyDefaults(&cfg)
	return &cfg, validate(&cfg)
}

func applyDefaults(cfg *Config) {
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.REST.BaseURL == "" {
		cfg.REST.BaseURL = "https://api.hyperliquid.xyz"
	}
	if cfg.REST.Timeout == 0 {
		cfg.REST.Timeout = 10 * time.Second
	}
	if cfg.WS.URL == "" {
		cfg.WS.URL = "wss://api.hyperliquid.xyz/ws"
	}
	if cfg.WS.ReconnectDelay == 0 {
		cfg.WS.ReconnectDelay = 3 * time.Second
	}
	if cfg.State.SQLitePath == "" {
		cfg.State.SQLitePath = "data/hl-carry-bot.db"
	}
	if cfg.Strategy.EntryInterval == 0 {
		cfg.Strategy.EntryInterval = 30 * time.Second
	}
	if cfg.Strategy.CandleInterval == "" {
		cfg.Strategy.CandleInterval = "1h"
	}
	if cfg.Strategy.CandleWindow == 0 {
		cfg.Strategy.CandleWindow = 24
	}
}

func validate(cfg *Config) error {
	if cfg.Strategy.Asset == "" {
		return errors.New("strategy.asset is required")
	}
	if cfg.Strategy.NotionalUSD <= 0 {
		return errors.New("strategy.notional_usd must be > 0")
	}
	if cfg.Risk.MaxNotionalUSD > 0 && cfg.Strategy.NotionalUSD > cfg.Risk.MaxNotionalUSD {
		return errors.New("strategy.notional_usd exceeds risk.max_notional_usd")
	}
	return nil
}
