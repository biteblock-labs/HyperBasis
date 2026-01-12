package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hl-carry-bot/internal/account"
	"hl-carry-bot/internal/config"
	"hl-carry-bot/internal/hl/exchange"
	"hl-carry-bot/internal/hl/rest"
	"hl-carry-bot/internal/logging"
	"hl-carry-bot/internal/market"
	"hl-carry-bot/internal/state/sqlite"

	"go.uber.org/zap"
)

const (
	defaultVerifyNotional = 5.0
	defaultSlippageBps    = 20
	defaultRESTTimeout    = 10 * time.Second
	defaultRESTBaseURL    = "https://api.hyperliquid.xyz"
	defaultLimitTif       = exchange.TifIoc
	defaultVerifyEnvFile  = ".env"
)

func main() {
	configPath := flag.String("config", "", "optional config path for REST settings")
	dryRun := flag.Bool("dry-run", false, "print the derived order and exit")
	userFunding := flag.Bool("user-funding", false, "fetch and print /info userFunding and exit")
	fundingStartMS := flag.Int64("funding-start-ms", 0, "startTime (ms since epoch) for userFunding query")
	fundingHours := flag.Int("funding-hours", 0, "lookback hours for userFunding query (used if funding-start-ms is 0)")
	flag.Parse()

	if err := config.LoadEnv(defaultVerifyEnvFile); err != nil {
		fatal(err)
	}

	logCfg := config.LoggingConfig{Level: "info"}
	baseURL := defaultRESTBaseURL
	timeout := defaultRESTTimeout
	var cfg *config.Config
	if *configPath != "" {
		loaded, err := config.Load(*configPath)
		if err != nil {
			fatal(err)
		}
		cfg = loaded
		logCfg = cfg.Log
		if cfg.REST.BaseURL != "" {
			baseURL = cfg.REST.BaseURL
		}
		if cfg.REST.Timeout > 0 {
			timeout = cfg.REST.Timeout
		}
	}

	log := logging.New(logCfg)
	defer func() { _ = log.Sync() }()

	if *userFunding {
		runUserFunding(log, baseURL, timeout, *fundingStartMS, *fundingHours)
		return
	}

	asset := strings.TrimSpace(os.Getenv("HL_VERIFY_ASSET"))
	if asset == "" && cfg != nil {
		if cfg.Strategy.SpotAsset != "" {
			asset = cfg.Strategy.SpotAsset
		} else {
			asset = cfg.Strategy.Asset
		}
	}
	if asset == "" {
		fatal(errors.New("HL_VERIFY_ASSET is required"))
	}
	wallet := strings.TrimSpace(os.Getenv("HL_WALLET_ADDRESS"))
	if wallet == "" {
		fatal(errors.New("HL_WALLET_ADDRESS is required"))
	}
	privateKey := strings.TrimSpace(os.Getenv("HL_PRIVATE_KEY"))
	if privateKey == "" {
		fatal(errors.New("HL_PRIVATE_KEY is required"))
	}

	notional := defaultVerifyNotional
	if envVal, ok, err := floatEnv("HL_VERIFY_NOTIONAL"); err != nil {
		fatal(err)
	} else if ok {
		notional = envVal
	} else if cfg != nil && cfg.Strategy.NotionalUSD > 0 {
		notional = cfg.Strategy.NotionalUSD
	}

	limitPrice := 0.0
	if envVal, ok, err := floatEnv("HL_VERIFY_LIMIT_PRICE"); err != nil {
		fatal(err)
	} else if ok {
		limitPrice = envVal
	}

	slippageBps := defaultSlippageBps
	if envVal, ok, err := intEnv("HL_VERIFY_SLIPPAGE_BPS"); err != nil {
		fatal(err)
	} else if ok {
		slippageBps = envVal
	}

	isMainnet := !strings.Contains(strings.ToLower(baseURL), "testnet")
	signer, err := exchange.NewSigner(privateKey, isMainnet)
	if err != nil {
		fatal(err)
	}
	if !strings.EqualFold(wallet, signer.Address().Hex()) {
		fatal(fmt.Errorf("wallet address does not match private key: got %s expected %s", wallet, signer.Address().Hex()))
	}

	restClient := rest.New(baseURL, timeout, log)
	md := market.New(restClient, nil, log)
	ctx := context.Background()
	if err := md.RefreshContexts(ctx); err != nil {
		fatal(err)
	}

	spotCtx, ok := md.SpotContext(asset)
	if !ok {
		spotCtx, ok = md.SpotContext(asset + "/USDC")
	}
	if !ok {
		fatal(fmt.Errorf("spot asset not found for %s", asset))
	}
	spotID, ok := md.SpotAssetID(spotCtx.Symbol)
	if !ok {
		spotID, ok = md.SpotAssetID(asset)
	}
	if !ok {
		fatal(fmt.Errorf("spot asset id not found for %s", asset))
	}

	if limitPrice <= 0 {
		mid, err := midWithFallback(ctx, md, spotCtx, asset)
		if err != nil {
			fatal(err)
		}
		limitPrice = mid * (1 + float64(slippageBps)/10000.0)
	}
	if limitPrice <= 0 {
		fatal(errors.New("limit price must be > 0"))
	}
	limitPrice = normalizeLimitPrice(limitPrice, true, spotCtx.BaseSzDecimals)
	if limitPrice <= 0 {
		fatal(errors.New("limit price <= 0 after tick rounding"))
	}

	size := notional / limitPrice
	if spotCtx.BaseSzDecimals >= 0 {
		size = roundDown(size, spotCtx.BaseSzDecimals)
	}
	if size <= 0 {
		fatal(errors.New("calculated size <= 0 after rounding"))
	}

	order, err := exchange.LimitOrderWire(spotID, true, size, limitPrice, false, defaultLimitTif, "")
	if err != nil {
		fatal(err)
	}

	fmt.Printf("verify order: asset=%s spot_id=%d size=%s limit_price=%s notional=%.6f\n", spotCtx.Symbol, spotID, order.Size, order.Price, size*limitPrice)
	if *dryRun {
		return
	}

	exClient, err := exchange.NewClient(baseURL, timeout, signer, "")
	if err != nil {
		fatal(err)
	}
	statePath := "data/hl-carry-bot.db"
	if cfg != nil && cfg.State.SQLitePath != "" {
		statePath = cfg.State.SQLitePath
	}
	if statePath != "" {
		if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
			log.Warn("nonce store init failed: " + err.Error())
		} else if store, err := sqlite.New(statePath); err != nil {
			log.Warn("nonce store init failed: " + err.Error())
		} else {
			defer store.Close()
			if err := exClient.InitNonceStore(ctx, store); err != nil {
				log.Warn("nonce store init failed: " + err.Error())
			}
		}
	}
	resp, err := exClient.PlaceOrder(ctx, order)
	if err != nil {
		fatal(err)
	}
	orderID := exchange.OrderIDFromResponse(resp)
	if orderID != "" {
		fmt.Printf("exchange response: order_id=%s\n", orderID)
		return
	}
	fmt.Printf("exchange response: %v\n", resp)
}

func runUserFunding(log *zap.Logger, baseURL string, timeout time.Duration, startTimeMS int64, lookbackHours int) {
	wallet := strings.TrimSpace(os.Getenv("HL_WALLET_ADDRESS"))
	if wallet == "" {
		fatal(errors.New("HL_WALLET_ADDRESS is required"))
	}
	if startTimeMS <= 0 && lookbackHours > 0 {
		startTimeMS = time.Now().Add(-time.Duration(lookbackHours) * time.Hour).UnixMilli()
	}
	req := map[string]any{
		"type": "userFunding",
		"user": wallet,
	}
	if startTimeMS > 0 {
		req["startTime"] = startTimeMS
	}
	restClient := rest.New(baseURL, timeout, log)
	accountClient := account.New(restClient, nil, log, wallet)
	queryStart := startTimeMS
	if queryStart <= 0 {
		queryStart = -1
	}
	entries, err := accountClient.UserFunding(context.Background(), queryStart)
	if err != nil {
		fatal(err)
	}
	entryCount := 0
	if entries != nil {
		entryCount = len(entries)
	}
	fmt.Printf("userFunding entries: %d\n", entryCount)
	payload, err := restClient.InfoAny(context.Background(), req)
	if err != nil {
		fatal(err)
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fatal(err)
	}
	fmt.Printf("userFunding response:\n%s\n", string(pretty))
}

func midWithFallback(ctx context.Context, md *market.MarketData, spotCtx market.SpotContext, asset string) (float64, error) {
	if spotCtx.MidKey != "" {
		if mid, err := md.Mid(ctx, spotCtx.MidKey); err == nil {
			return mid, nil
		}
	}
	if spotCtx.Symbol != "" {
		if mid, err := md.Mid(ctx, spotCtx.Symbol); err == nil {
			return mid, nil
		}
	}
	if asset != "" {
		if mid, err := md.Mid(ctx, asset); err == nil {
			return mid, nil
		}
	}
	return 0, errors.New("mid price not found")
}

func floatEnv(key string) (float64, bool, error) {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed, true, nil
}

func intEnv(key string) (int, bool, error) {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return 0, false, nil
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed, true, nil
}

func roundDown(value float64, decimals int) float64 {
	if decimals <= 0 {
		return math.Floor(value)
	}
	factor := math.Pow10(decimals)
	return math.Floor(value*factor) / factor
}

func roundTo(value float64, decimals int) float64 {
	if decimals <= 0 {
		return math.Round(value)
	}
	factor := math.Pow10(decimals)
	return math.Round(value*factor) / factor
}

func normalizeLimitPrice(price float64, isSpot bool, szDecimals int) float64 {
	if price == 0 {
		return 0
	}
	// Hyperliquid enforces a tick size that is effectively a combination of
	// (a) 5 significant figures and (b) a decimal precision derived from szDecimals.
	if sig, err := strconv.ParseFloat(strconv.FormatFloat(price, 'g', 5, 64), 64); err == nil {
		price = sig
	}
	decimals := 6
	if isSpot {
		decimals = 8
	}
	if szDecimals >= 0 {
		decimals -= szDecimals
		if decimals < 0 {
			decimals = 0
		}
	}
	return roundTo(price, decimals)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
