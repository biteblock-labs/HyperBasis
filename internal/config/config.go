package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Log       LoggingConfig   `yaml:"log"`
	REST      RESTConfig      `yaml:"rest"`
	WS        WSConfig        `yaml:"ws"`
	State     StateConfig     `yaml:"state"`
	Metrics   MetricsConfig   `yaml:"metrics"`
	Timescale TimescaleConfig `yaml:"timescale"`
	Strategy  StrategyConfig  `yaml:"strategy"`
	Risk      RiskConfig      `yaml:"risk"`
	Telegram  TelegramConfig  `yaml:"telegram"`
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
	PingInterval   time.Duration `yaml:"ping_interval"`
}

type StateConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

type MetricsConfig struct {
	Enabled *bool  `yaml:"enabled"`
	Address string `yaml:"address"`
	Path    string `yaml:"path"`
}

type TimescaleConfig struct {
	Enabled         bool          `yaml:"enabled"`
	DSN             string        `yaml:"dsn"`
	Schema          string        `yaml:"schema"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	QueueSize       int           `yaml:"queue_size"`
}

func (m MetricsConfig) EnabledValue() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

type StrategyConfig struct {
	Asset                   string        `yaml:"asset"`
	PerpAsset               string        `yaml:"perp_asset"`
	SpotAsset               string        `yaml:"spot_asset"`
	NotionalUSD             float64       `yaml:"notional_usd"`
	MinFundingRate          float64       `yaml:"min_funding_rate"`
	MaxVolatility           float64       `yaml:"max_volatility"`
	FeeBps                  float64       `yaml:"fee_bps"`
	SlippageBps             float64       `yaml:"slippage_bps"`
	IOCPriceBps             float64       `yaml:"ioc_price_bps"`
	CarryBufferUSD          float64       `yaml:"carry_buffer_usd"`
	FundingConfirmations    int           `yaml:"funding_confirmations"`
	FundingDipConfirmations int           `yaml:"funding_dip_confirmations"`
	DeltaBandUSD            float64       `yaml:"delta_band_usd"`
	MinExposureUSD          float64       `yaml:"min_exposure_usd"`
	EntryInterval           time.Duration `yaml:"entry_interval"`
	EntryCooldown           time.Duration `yaml:"entry_cooldown"`
	HedgeCooldown           time.Duration `yaml:"hedge_cooldown"`
	SpotReconcileInterval   time.Duration `yaml:"spot_reconcile_interval"`
	EntryTimeout            time.Duration `yaml:"entry_timeout"`
	EntryPollInterval       time.Duration `yaml:"entry_poll_interval"`
	ExitOnFundingDip        bool          `yaml:"exit_on_funding_dip"`
	ExitFundingGuard        time.Duration `yaml:"exit_funding_guard"`
	ExitFundingGuardEnabled *bool         `yaml:"exit_funding_guard_enabled"`
	CandleInterval          string        `yaml:"candle_interval"`
	CandleWindow            int           `yaml:"candle_window"`
}

type RiskConfig struct {
	MaxNotionalUSD float64       `yaml:"max_notional_usd"`
	MaxOpenOrders  int           `yaml:"max_open_orders"`
	MinMarginRatio float64       `yaml:"min_margin_ratio"`
	MinHealthRatio float64       `yaml:"min_health_ratio"`
	MaxMarketAge   time.Duration `yaml:"max_market_age"`
	MaxAccountAge  time.Duration `yaml:"max_account_age"`
}

type TelegramConfig struct {
	Enabled                bool          `yaml:"enabled"`
	Token                  string        `yaml:"token"`
	ChatID                 string        `yaml:"chat_id"`
	OperatorEnabled        bool          `yaml:"operator_enabled"`
	OperatorPollInterval   time.Duration `yaml:"operator_poll_interval"`
	OperatorAllowedUserIDs []int64       `yaml:"operator_allowed_user_ids"`
}

const (
	// Observed Hyperliquid minimum order value on mainnet.
	minOrderValueUSD = 10.0

	minDeltaBandUSD = 2.0
	deltaBandRatio  = 0.05
)

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
	applyEnvOverrides(&cfg)
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
		if derived := deriveWSURL(cfg.REST.BaseURL); derived != "" {
			cfg.WS.URL = derived
		} else {
			cfg.WS.URL = "wss://api.hyperliquid.xyz/ws"
		}
	}
	if cfg.WS.ReconnectDelay == 0 {
		cfg.WS.ReconnectDelay = 3 * time.Second
	}
	if cfg.WS.PingInterval == 0 {
		cfg.WS.PingInterval = 50 * time.Second
	}
	if cfg.State.SQLitePath == "" {
		cfg.State.SQLitePath = "data/hl-carry-bot.db"
	}
	if cfg.Metrics.Enabled == nil {
		enabled := true
		cfg.Metrics.Enabled = &enabled
	}
	if cfg.Metrics.Address == "" {
		cfg.Metrics.Address = "127.0.0.1:9001"
	}
	if cfg.Metrics.Path == "" {
		cfg.Metrics.Path = "/metrics"
	}
	if cfg.Timescale.Schema == "" {
		cfg.Timescale.Schema = "public"
	}
	if cfg.Timescale.QueueSize == 0 {
		cfg.Timescale.QueueSize = 256
	}
	if cfg.Timescale.MaxOpenConns == 0 {
		cfg.Timescale.MaxOpenConns = 5
	}
	if cfg.Timescale.MaxIdleConns == 0 {
		cfg.Timescale.MaxIdleConns = 5
	}
	if cfg.Timescale.ConnMaxLifetime == 0 {
		cfg.Timescale.ConnMaxLifetime = 5 * time.Minute
	}
	if cfg.Telegram.OperatorPollInterval == 0 {
		cfg.Telegram.OperatorPollInterval = 3 * time.Second
	}
	if cfg.Strategy.EntryInterval == 0 {
		cfg.Strategy.EntryInterval = 30 * time.Second
	}
	if cfg.Strategy.EntryCooldown == 0 {
		if cfg.Strategy.EntryInterval > 0 {
			cfg.Strategy.EntryCooldown = cfg.Strategy.EntryInterval * 2
		} else {
			cfg.Strategy.EntryCooldown = 60 * time.Second
		}
	}
	if cfg.Strategy.HedgeCooldown == 0 {
		cfg.Strategy.HedgeCooldown = 10 * time.Second
	}
	if cfg.Strategy.SpotReconcileInterval == 0 {
		cfg.Strategy.SpotReconcileInterval = 5 * time.Minute
	}
	if cfg.Strategy.FundingConfirmations == 0 {
		cfg.Strategy.FundingConfirmations = 1
	}
	if cfg.Strategy.FundingDipConfirmations == 0 {
		cfg.Strategy.FundingDipConfirmations = 1
	}
	if cfg.Strategy.DeltaBandUSD == 0 {
		if derived := deriveDeltaBandUSD(cfg.Strategy.NotionalUSD); derived > 0 {
			cfg.Strategy.DeltaBandUSD = derived
		}
	}
	if cfg.Strategy.MinExposureUSD == 0 {
		cfg.Strategy.MinExposureUSD = deriveMinExposureUSD()
	}
	if cfg.Strategy.EntryTimeout == 0 {
		cfg.Strategy.EntryTimeout = 5 * time.Second
	}
	if cfg.Strategy.EntryPollInterval == 0 {
		cfg.Strategy.EntryPollInterval = 250 * time.Millisecond
	}
	if cfg.Strategy.ExitFundingGuard == 0 {
		cfg.Strategy.ExitFundingGuard = 2 * time.Minute
	}
	if cfg.Strategy.ExitFundingGuardEnabled == nil {
		enabled := true
		cfg.Strategy.ExitFundingGuardEnabled = &enabled
	}
	if cfg.Strategy.CandleInterval == "" {
		cfg.Strategy.CandleInterval = "1h"
	}
	if cfg.Strategy.CandleWindow == 0 {
		cfg.Strategy.CandleWindow = 24
	}
	if cfg.Strategy.PerpAsset == "" && cfg.Strategy.Asset != "" {
		cfg.Strategy.PerpAsset = cfg.Strategy.Asset
	}
	if cfg.Strategy.SpotAsset == "" {
		if cfg.Strategy.Asset != "" {
			cfg.Strategy.SpotAsset = cfg.Strategy.Asset
		} else if cfg.Strategy.PerpAsset != "" {
			cfg.Strategy.SpotAsset = cfg.Strategy.PerpAsset
		}
	}
	if cfg.Risk.MaxMarketAge == 0 {
		cfg.Risk.MaxMarketAge = deriveMaxMarketAge(cfg.Strategy.EntryInterval, cfg.WS.PingInterval)
	}
	if cfg.Risk.MaxAccountAge == 0 {
		cfg.Risk.MaxAccountAge = deriveMaxAccountAge(cfg.Strategy.EntryInterval, cfg.WS.PingInterval, cfg.Strategy.SpotReconcileInterval)
	}
}

func applyEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	if dsn := strings.TrimSpace(os.Getenv("HL_TIMESCALE_DSN")); dsn != "" {
		cfg.Timescale.DSN = dsn
	}
	if token := strings.TrimSpace(os.Getenv("HL_TELEGRAM_TOKEN")); token != "" {
		cfg.Telegram.Token = token
	}
	if chatID := strings.TrimSpace(os.Getenv("HL_TELEGRAM_CHAT_ID")); chatID != "" {
		cfg.Telegram.ChatID = chatID
	}
}

func deriveWSURL(restBase string) string {
	restBase = strings.TrimSpace(restBase)
	if restBase == "" {
		return ""
	}
	parsed, err := url.Parse(restBase)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		parsed.Path = "/ws"
	} else {
		parsed.Path = path + "/ws"
	}
	return parsed.String()
}

func validate(cfg *Config) error {
	if cfg.Strategy.PerpAsset == "" {
		return errors.New("strategy.perp_asset is required")
	}
	if cfg.Strategy.SpotAsset == "" {
		return errors.New("strategy.spot_asset is required")
	}
	if cfg.Strategy.NotionalUSD <= 0 {
		return errors.New("strategy.notional_usd must be > 0")
	}
	if cfg.Strategy.EntryTimeout <= 0 {
		return errors.New("strategy.entry_timeout must be > 0")
	}
	if cfg.Strategy.EntryPollInterval <= 0 {
		return errors.New("strategy.entry_poll_interval must be > 0")
	}
	if cfg.Strategy.MinExposureUSD < 0 {
		return errors.New("strategy.min_exposure_usd must be >= 0")
	}
	if cfg.Strategy.FeeBps < 0 {
		return errors.New("strategy.fee_bps must be >= 0")
	}
	if cfg.Strategy.SlippageBps < 0 {
		return errors.New("strategy.slippage_bps must be >= 0")
	}
	if cfg.Strategy.IOCPriceBps < 0 {
		return errors.New("strategy.ioc_price_bps must be >= 0")
	}
	if cfg.Strategy.CarryBufferUSD < 0 {
		return errors.New("strategy.carry_buffer_usd must be >= 0")
	}
	if cfg.Strategy.FundingConfirmations < 1 {
		return errors.New("strategy.funding_confirmations must be >= 1")
	}
	if cfg.Strategy.FundingDipConfirmations < 1 {
		return errors.New("strategy.funding_dip_confirmations must be >= 1")
	}
	if cfg.Strategy.DeltaBandUSD < 0 {
		return errors.New("strategy.delta_band_usd must be >= 0")
	}
	if cfg.Strategy.EntryCooldown < 0 {
		return errors.New("strategy.entry_cooldown must be >= 0")
	}
	if cfg.Strategy.HedgeCooldown < 0 {
		return errors.New("strategy.hedge_cooldown must be >= 0")
	}
	if cfg.Strategy.SpotReconcileInterval < 0 {
		return errors.New("strategy.spot_reconcile_interval must be >= 0")
	}
	if cfg.Strategy.ExitFundingGuard < 0 {
		return errors.New("strategy.exit_funding_guard must be >= 0")
	}
	if cfg.Metrics.Path == "" || !strings.HasPrefix(cfg.Metrics.Path, "/") {
		return errors.New("metrics.path must start with /")
	}
	if cfg.Timescale.Enabled {
		if strings.TrimSpace(cfg.Timescale.DSN) == "" {
			return errors.New("timescale.dsn is required when timescale.enabled is true")
		}
		if cfg.Timescale.QueueSize <= 0 {
			return errors.New("timescale.queue_size must be > 0")
		}
		if cfg.Timescale.MaxOpenConns < 0 {
			return errors.New("timescale.max_open_conns must be >= 0")
		}
		if cfg.Timescale.MaxIdleConns < 0 {
			return errors.New("timescale.max_idle_conns must be >= 0")
		}
		if cfg.Timescale.ConnMaxLifetime < 0 {
			return errors.New("timescale.conn_max_lifetime must be >= 0")
		}
		if !isValidIdentifier(cfg.Timescale.Schema) {
			return errors.New("timescale.schema must be alphanumeric/underscore and start with a letter or underscore")
		}
	}
	if cfg.Risk.MinMarginRatio < 0 {
		return errors.New("risk.min_margin_ratio must be >= 0")
	}
	if cfg.Risk.MinHealthRatio < 0 {
		return errors.New("risk.min_health_ratio must be >= 0")
	}
	if cfg.Risk.MaxMarketAge < 0 {
		return errors.New("risk.max_market_age must be >= 0")
	}
	if cfg.Risk.MaxAccountAge < 0 {
		return errors.New("risk.max_account_age must be >= 0")
	}
	if cfg.Risk.MaxNotionalUSD > 0 && cfg.Strategy.NotionalUSD > cfg.Risk.MaxNotionalUSD {
		return errors.New("strategy.notional_usd exceeds risk.max_notional_usd")
	}
	if cfg.Telegram.Enabled {
		if strings.TrimSpace(cfg.Telegram.Token) == "" || strings.TrimSpace(cfg.Telegram.ChatID) == "" {
			return errors.New("telegram token and chat_id are required when telegram.enabled is true (set HL_TELEGRAM_TOKEN and HL_TELEGRAM_CHAT_ID)")
		}
	}
	if cfg.Telegram.OperatorEnabled {
		if !cfg.Telegram.Enabled {
			return errors.New("telegram.operator_enabled requires telegram.enabled to be true")
		}
		if cfg.Telegram.OperatorPollInterval <= 0 {
			return errors.New("telegram.operator_poll_interval must be > 0")
		}
		if strings.TrimSpace(cfg.Telegram.ChatID) == "" {
			return errors.New("telegram.chat_id is required when telegram.operator_enabled is true")
		}
		if _, err := strconv.ParseInt(strings.TrimSpace(cfg.Telegram.ChatID), 10, 64); err != nil {
			return errors.New("telegram.chat_id must be numeric when telegram.operator_enabled is true")
		}
	}
	return nil
}

func isValidIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if r >= '0' && r <= '9' {
			if i == 0 {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func deriveMinExposureUSD() float64 {
	return minOrderValueUSD
}

func deriveDeltaBandUSD(notionalUSD float64) float64 {
	if notionalUSD <= 0 {
		return 0
	}
	band := notionalUSD * deltaBandRatio
	if band < minDeltaBandUSD {
		return minDeltaBandUSD
	}
	return band
}

func deriveMaxMarketAge(entryInterval, pingInterval time.Duration) time.Duration {
	return maxDuration(
		scaleDuration(entryInterval, 4),
		scaleDuration(pingInterval, 2),
	)
}

func deriveMaxAccountAge(entryInterval, pingInterval, spotReconcileInterval time.Duration) time.Duration {
	return maxDuration(
		scaleDuration(spotReconcileInterval, 2),
		scaleDuration(entryInterval, 4),
		scaleDuration(pingInterval, 2),
	)
}

func scaleDuration(value time.Duration, multiplier int) time.Duration {
	if value <= 0 || multiplier <= 0 {
		return 0
	}
	return value * time.Duration(multiplier)
}

func maxDuration(values ...time.Duration) time.Duration {
	max := time.Duration(0)
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
