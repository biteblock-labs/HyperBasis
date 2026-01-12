package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hl-carry-bot/internal/account"
	"hl-carry-bot/internal/alerts"
	"hl-carry-bot/internal/config"
	"hl-carry-bot/internal/exec"
	"hl-carry-bot/internal/hl/exchange"
	"hl-carry-bot/internal/hl/rest"
	"hl-carry-bot/internal/hl/ws"
	"hl-carry-bot/internal/market"
	"hl-carry-bot/internal/metrics"
	persist "hl-carry-bot/internal/state"
	"hl-carry-bot/internal/state/sqlite"
	"hl-carry-bot/internal/strategy"

	"go.uber.org/zap"
)

type App struct {
	cfg           *config.Config
	log           *zap.Logger
	store         *sqlite.Store
	rest          *rest.Client
	ws            *ws.Client
	exchange      *exchange.Client
	market        *market.MarketData
	account       *account.Account
	executor      *exec.Executor
	metrics       *metrics.Metrics
	metricsServer *http.Server
	metricsAddr   string
	metricsPath   string
	alerts        *alerts.Telegram
	strategy      *strategy.StateMachine

	snapshotPersistWarned   bool
	spotRefreshWarned       bool
	killSwitchActive        bool
	fundingOKCount          int
	fundingBadCount         int
	fundingForecastWarned   bool
	fundingReceiptWarned    bool
	entryCooldownUntil      time.Time
	hedgeCooldownUntil      time.Time
	lastFundingReceiptCheck time.Time
	lastFundingReceiptAt    time.Time
}

const (
	entryFillLookback            = 2 * time.Second
	flatEpsilon                  = 1e-6
	fundingReceiptGrace          = 15 * time.Second
	fundingReceiptCheckInterval  = 30 * time.Second
	fundingReceiptLookback       = 6 * time.Hour
	fundingReceiptLookbackBuffer = 1 * time.Minute
)

func New(cfg *config.Config, log *zap.Logger) (*App, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.State.SQLitePath), 0o755); err != nil {
		return nil, err
	}
	store, err := sqlite.New(cfg.State.SQLitePath)
	if err != nil {
		return nil, err
	}
	restClient := rest.New(cfg.REST.BaseURL, cfg.REST.Timeout, log)
	wsClient := ws.New(cfg.WS.URL, cfg.WS.ReconnectDelay, cfg.WS.PingInterval, log)
	marketData := market.New(restClient, wsClient, log)
	marketData.EnableCandle(cfg.Strategy.PerpAsset, cfg.Strategy.CandleInterval, cfg.Strategy.CandleWindow)

	walletAddress := strings.TrimSpace(os.Getenv("HL_WALLET_ADDRESS"))
	if walletAddress == "" {
		return nil, errors.New("HL_WALLET_ADDRESS is required")
	}
	privateKey := strings.TrimSpace(os.Getenv("HL_PRIVATE_KEY"))
	if privateKey == "" {
		return nil, errors.New("HL_PRIVATE_KEY is required")
	}
	accountAddress := strings.TrimSpace(os.Getenv("HL_ACCOUNT_ADDRESS"))
	if accountAddress == "" {
		accountAddress = walletAddress
	}
	vaultAddress := strings.TrimSpace(os.Getenv("HL_VAULT_ADDRESS"))
	isMainnet := !strings.Contains(strings.ToLower(cfg.REST.BaseURL), "testnet")
	signer, err := exchange.NewSigner(privateKey, isMainnet)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(walletAddress, signer.Address().Hex()) {
		return nil, fmt.Errorf("wallet address does not match private key: got %s expected %s", walletAddress, signer.Address().Hex())
	}
	exClient, err := exchange.NewClient(cfg.REST.BaseURL, cfg.REST.Timeout, signer, vaultAddress)
	if err != nil {
		return nil, err
	}
	exClient.SetLogger(log)

	accountWS := ws.New(cfg.WS.URL, cfg.WS.ReconnectDelay, cfg.WS.PingInterval, log)
	accountClient := account.New(restClient, accountWS, log, accountAddress)
	executor := exec.New(&exchangeAdapter{client: exClient, tif: exchange.TifGtc, log: log}, store, log)
	metricsClient := metrics.NewNoop()
	var metricsServer *http.Server
	metricsAddr := ""
	metricsPath := ""
	if cfg.Metrics.EnabledValue() {
		prom := metrics.NewPrometheus()
		metricsClient = prom.Metrics
		metricsAddr = cfg.Metrics.Address
		metricsPath = cfg.Metrics.Path
		mux := http.NewServeMux()
		mux.Handle(metricsPath, prom.Handler())
		metricsServer = &http.Server{
			Addr:    metricsAddr,
			Handler: mux,
		}
	}
	alertsClient := alerts.NewTelegram(cfg.Telegram, log)
	return &App{
		cfg:           cfg,
		log:           log,
		store:         store,
		rest:          restClient,
		ws:            wsClient,
		exchange:      exClient,
		market:        marketData,
		account:       accountClient,
		executor:      executor,
		metrics:       metricsClient,
		metricsServer: metricsServer,
		metricsAddr:   metricsAddr,
		metricsPath:   metricsPath,
		alerts:        alertsClient,
		strategy:      strategy.NewStateMachine(),
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	defer a.store.Close()
	a.startMetricsServer(ctx)
	if a.exchange != nil && a.store != nil {
		if err := a.exchange.InitNonceStore(ctx, a.store); err != nil {
			a.log.Warn("nonce store init failed", zap.Error(err))
		} else if state, ok := a.exchange.NonceState(); ok {
			a.log.Info("nonce persistence enabled", zap.String("nonce_key", state.Key), zap.Uint64("nonce_seed", state.Last))
		}
	}
	if a.log != nil {
		a.log.Info("startup: reconciling account state")
	}
	state, err := a.account.Reconcile(ctx)
	if err != nil {
		return err
	}
	if err := a.market.RefreshContexts(ctx); err != nil {
		a.log.Warn("context refresh failed", zap.Error(err))
	}
	restored, ok, err := persist.LoadStrategySnapshot(ctx, a.store)
	if err != nil {
		a.log.Warn("strategy snapshot load failed", zap.Error(err))
	} else if ok {
		a.log.Info("loaded strategy snapshot",
			zap.String("action", restored.Action),
			zap.String("spot_asset", restored.SpotAsset),
			zap.String("perp_asset", restored.PerpAsset),
			zap.Float64("spot_mid_price", restored.SpotMidPrice),
			zap.Float64("perp_mid_price", restored.PerpMidPrice),
			zap.Float64("spot_balance", restored.SpotBalance),
			zap.Float64("perp_position", restored.PerpPosition),
			zap.Int("open_orders", restored.OpenOrders),
			zap.Int64("updated_at_ms", restored.UpdatedAtMS),
		)
	}
	a.log.Info("reconciled state",
		zap.Any("spot_balances", state.SpotBalances),
		zap.Any("perp_positions", state.PerpPosition),
		zap.Int("open_orders", len(state.OpenOrders)),
	)
	if len(state.OpenOrders) > 0 {
		a.cancelOpenOrders(ctx, state.OpenOrders)
	}
	a.restoreStrategyState(state, restored, ok)
	spotMidPrice := restored.SpotMidPrice
	perpMidPrice := restored.PerpMidPrice
	if a.cfg != nil {
		if mid, _, err := a.spotMid(ctx, a.cfg.Strategy.SpotAsset); err == nil && mid > 0 {
			spotMidPrice = mid
		}
		if mid, err := a.market.Mid(ctx, a.cfg.Strategy.PerpAsset); err == nil && mid > 0 {
			perpMidPrice = mid
		}
	}
	a.persistStrategySnapshot(ctx, strategy.MarketSnapshot{
		PerpAsset:      a.cfg.Strategy.PerpAsset,
		SpotAsset:      a.cfg.Strategy.SpotAsset,
		SpotMidPrice:   spotMidPrice,
		PerpMidPrice:   perpMidPrice,
		SpotBalance:    a.spotBalanceForAsset(a.cfg.Strategy.SpotAsset, state.SpotBalances),
		PerpPosition:   state.PerpPosition[a.cfg.Strategy.PerpAsset],
		OpenOrderCount: len(state.OpenOrders),
	})
	if err := a.account.Start(ctx); err != nil {
		return err
	}
	if a.log != nil {
		a.log.Info("startup: account ws started")
	}
	a.startSpotReconciler(ctx)
	if err := a.market.Start(ctx); err != nil {
		return err
	}
	if a.log != nil {
		a.log.Info("startup: market ws started")
	}
	if err := a.market.RefreshContexts(ctx); err != nil {
		a.log.Warn("context refresh failed", zap.Error(err))
	}
	a.refreshFundingForecast(ctx)
	if a.log != nil {
		a.log.Info("startup: complete")
	}

	ticker := time.NewTicker(a.cfg.Strategy.EntryInterval)
	defer ticker.Stop()
	if a.log != nil {
		a.log.Info("strategy loop started", zap.Duration("entry_interval", a.cfg.Strategy.EntryInterval))
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.tick(ctx); err != nil {
				a.log.Warn("strategy tick failed", zap.Error(err))
			}
		}
	}
}

func (a *App) tick(ctx context.Context) error {
	if err := a.market.RefreshContexts(ctx); err != nil {
		a.log.Warn("context refresh failed", zap.Error(err))
	}
	a.refreshFundingForecast(ctx)
	perpAsset := a.cfg.Strategy.PerpAsset
	spotAsset := a.cfg.Strategy.SpotAsset
	spotMid, spotCtx, err := a.spotMid(ctx, spotAsset)
	if err != nil {
		return err
	}
	perpMid, _ := a.market.Mid(ctx, perpAsset)
	oraclePrice, _ := a.market.OraclePrice(perpAsset)
	funding, _ := a.market.FundingRate(perpAsset)
	vol, _ := a.market.Volatility(perpAsset)

	accountSnap := a.account.Snapshot()
	spotBalance := 0.0
	if spotCtx.Base != "" {
		spotBalance = accountSnap.SpotBalances[spotCtx.Base]
	} else {
		spotBalance = accountSnap.SpotBalances[spotAsset]
	}
	perpPosition := accountSnap.PerpPosition[perpAsset]

	snap := strategy.MarketSnapshot{
		PerpAsset:      perpAsset,
		SpotAsset:      spotAsset,
		SpotMidPrice:   spotMid,
		PerpMidPrice:   perpMid,
		OraclePrice:    oraclePrice,
		FundingRate:    funding,
		Volatility:     vol,
		NotionalUSD:    a.cfg.Strategy.NotionalUSD,
		SpotBalance:    spotBalance,
		PerpPosition:   perpPosition,
		OpenOrderCount: len(accountSnap.OpenOrders),
	}
	if accountSnap.HasMarginSummary {
		snap.MarginRatio = accountSnap.MarginSummary.MarginRatio
		snap.HealthRatio = accountSnap.MarginSummary.HealthRatio
		snap.HasMarginRatio = accountSnap.MarginSummary.HasMarginRatio
		snap.HasHealthRatio = accountSnap.MarginSummary.HasHealthRatio
	}
	defer a.persistStrategySnapshot(ctx, snap)
	flatStrict := isFlat(spotBalance, perpPosition)
	flat := a.isExposureFlat(spotBalance, perpPosition, spotMid, perpMid)
	spotExposureUSD := math.Abs(spotBalance) * spotMid
	perpExposureUSD := math.Abs(perpPosition) * perpMid
	priceRef := snap.OraclePrice
	if priceRef == 0 {
		priceRef = snap.PerpMidPrice
	}
	if priceRef == 0 {
		priceRef = snap.SpotMidPrice
	}
	deltaUSD := (spotBalance + perpPosition) * priceRef
	marketAge := time.Duration(0)
	if a.market != nil {
		marketAge = time.Since(a.market.LastMidUpdate())
	}
	accountAge := time.Duration(0)
	if a.account != nil {
		accountAge = time.Since(a.account.LastUpdate())
	}
	now := time.Now().UTC()
	entryCooldownActive := a.entryCooldownActive(now)
	hedgeCooldownActive := a.hedgeCooldownActive(now)
	forecast, hasForecast := a.market.FundingForecast(perpAsset)
	forecastAge := time.Duration(0)
	if hasForecast && !forecast.ObservedAt.IsZero() {
		forecastAge = time.Since(forecast.ObservedAt)
	}
	minExpectedFunding := snap.NotionalUSD * a.cfg.Strategy.MinFundingRate
	expectedFunding := strategy.FundingPaymentEstimateUSD(snap)
	netCarryUSD, estimatedCostUSD := strategy.NetExpectedCarryUSD(snap, a.cfg.Strategy.FeeBps, a.cfg.Strategy.SlippageBps)
	carryBufferUSD := a.cfg.Strategy.CarryBufferUSD
	fundingRateOK := funding >= a.cfg.Strategy.MinFundingRate
	netCarryOK := netCarryUSD >= carryBufferUSD
	_, fundingOKConfirmed, fundingBadConfirmed := a.updateFundingRegime(funding, a.cfg.Strategy.MinFundingRate, netCarryUSD, carryBufferUSD)
	state := a.strategy.State
	logTick := func(decision string, extra ...zap.Field) {
		if a.log == nil {
			return
		}
		fields := []zap.Field{
			zap.String("state", string(state)),
			zap.String("decision", decision),
			zap.Bool("flat", flat),
			zap.Bool("flat_strict", flatStrict),
			zap.Int("open_orders", snap.OpenOrderCount),
			zap.Float64("spot_balance", spotBalance),
			zap.Float64("perp_position", perpPosition),
			zap.Float64("spot_mid", spotMid),
			zap.Float64("perp_mid", perpMid),
			zap.Float64("spot_exposure_usd", spotExposureUSD),
			zap.Float64("perp_exposure_usd", perpExposureUSD),
			zap.Float64("delta_usd", deltaUSD),
			zap.Float64("delta_band_usd", a.cfg.Strategy.DeltaBandUSD),
			zap.Float64("funding_rate", funding),
			zap.Float64("expected_funding_usd", expectedFunding),
			zap.Float64("min_expected_funding_usd", minExpectedFunding),
			zap.Float64("estimated_cost_usd", estimatedCostUSD),
			zap.Float64("net_expected_carry_usd", netCarryUSD),
			zap.Float64("carry_buffer_usd", carryBufferUSD),
			zap.Float64("fee_bps", a.cfg.Strategy.FeeBps),
			zap.Float64("slippage_bps", a.cfg.Strategy.SlippageBps),
			zap.Bool("funding_rate_ok", fundingRateOK),
			zap.Bool("net_carry_ok", netCarryOK),
			zap.Int("funding_ok_count", a.fundingOKCount),
			zap.Int("funding_bad_count", a.fundingBadCount),
			zap.Int("funding_confirmations", a.cfg.Strategy.FundingConfirmations),
			zap.Int("funding_dip_confirmations", a.cfg.Strategy.FundingDipConfirmations),
			zap.Float64("volatility", vol),
			zap.Float64("max_volatility", a.cfg.Strategy.MaxVolatility),
			zap.Float64("min_exposure_usd", a.cfg.Strategy.MinExposureUSD),
			zap.Float64("margin_ratio", snap.MarginRatio),
			zap.Float64("health_ratio", snap.HealthRatio),
			zap.Bool("has_margin_ratio", snap.HasMarginRatio),
			zap.Bool("has_health_ratio", snap.HasHealthRatio),
			zap.Bool("has_funding_forecast", hasForecast),
			zap.Float64("predicted_funding_rate", forecast.Rate),
			zap.Time("next_funding_at", forecast.NextFunding),
			zap.String("predicted_funding_source", forecast.Source),
			zap.Time("predicted_funding_observed_at", forecast.ObservedAt),
			zap.Duration("predicted_funding_age", forecastAge),
			zap.Duration("market_age", marketAge),
			zap.Duration("account_age", accountAge),
			zap.Bool("entry_cooldown_active", entryCooldownActive),
			zap.Bool("hedge_cooldown_active", hedgeCooldownActive),
		}
		fields = append(fields, extra...)
		a.log.Debug("tick", fields...)
	}
	if (state == strategy.StateEnter || state == strategy.StateExit) && snap.OpenOrderCount == 0 {
		if flat {
			a.resetToIdle()
		} else {
			a.strategy.Apply(strategy.EventHedgeOK)
		}
		state = a.strategy.State
	}
	if state == strategy.StateHedgeOK && flat {
		if !entryCooldownActive {
			a.resetToIdle()
			state = a.strategy.State
		}
	}
	if err := a.checkConnectivity(ctx, accountSnap.OpenOrders, marketAge, accountAge); err != nil {
		logTick("skip_connectivity", zap.Error(err))
		return nil
	}
	if state == strategy.StateIdle {
		if !flat || snap.OpenOrderCount > 0 {
			logTick("skip_idle_not_ready")
			return nil
		}
	}
	if err := strategy.CheckRisk(a.cfg.Risk, snap); err != nil {
		a.log.Warn("risk check failed", zap.Error(err))
		logTick("skip_risk", zap.Error(err))
		return nil
	}

	switch state {
	case strategy.StateIdle:
		enterSignal := fundingOKConfirmed && vol <= a.cfg.Strategy.MaxVolatility
		if enterSignal && entryCooldownActive {
			logTick("skip_entry_cooldown", zap.Bool("enter_signal", enterSignal), zap.Bool("funding_confirmed", fundingOKConfirmed))
			return nil
		}
		logTick("idle", zap.Bool("enter_signal", enterSignal), zap.Bool("funding_confirmed", fundingOKConfirmed))
		if enterSignal {
			if a.log != nil {
				a.log.Info("enter signal",
					zap.Float64("expected_funding_usd", expectedFunding),
					zap.Float64("min_expected_funding_usd", minExpectedFunding),
					zap.Float64("net_expected_carry_usd", netCarryUSD),
					zap.Float64("carry_buffer_usd", carryBufferUSD),
					zap.Float64("estimated_cost_usd", estimatedCostUSD),
					zap.Float64("volatility", vol),
					zap.Float64("max_volatility", a.cfg.Strategy.MaxVolatility),
				)
			}
			return a.enterPosition(ctx, snap)
		}
	case strategy.StateHedgeOK:
		exitSignal := a.cfg.Strategy.ExitOnFundingDip && fundingBadConfirmed
		exitGuarded := false
		timeToFunding := time.Duration(0)
		if exitSignal {
			exitGuarded, timeToFunding = a.shouldDeferExitForFunding(time.Now().UTC(), forecast, hasForecast, funding)
		}
		decision := "hedge_ok"
		if exitSignal {
			if exitGuarded {
				decision = "exit_guarded"
			} else {
				decision = "exit_signal"
			}
		}
		logTick(decision,
			zap.Bool("exit_signal", exitSignal),
			zap.Bool("exit_on_funding_dip", a.cfg.Strategy.ExitOnFundingDip),
			zap.Bool("funding_bad_confirmed", fundingBadConfirmed),
			zap.Bool("exit_guarded", exitGuarded),
			zap.Bool("exit_funding_guard_enabled", a.exitFundingGuardEnabled()),
			zap.Duration("exit_funding_guard", a.cfg.Strategy.ExitFundingGuard),
			zap.Duration("time_to_funding", timeToFunding),
		)
		if exitSignal && !exitGuarded {
			if a.log != nil {
				a.log.Info("exit signal",
					zap.Float64("expected_funding_usd", expectedFunding),
					zap.Float64("min_expected_funding_usd", minExpectedFunding),
					zap.Float64("net_expected_carry_usd", netCarryUSD),
					zap.Float64("carry_buffer_usd", carryBufferUSD),
					zap.Float64("estimated_cost_usd", estimatedCostUSD),
				)
			}
			return a.exitPosition(ctx, snap)
		}
		a.maybeLogFundingReceipt(ctx, now, snap, forecast, hasForecast)
		if hedgeCooldownActive {
			return nil
		}
		if err := a.rebalanceDelta(ctx, snap); err != nil {
			a.log.Warn("delta hedge failed", zap.Error(err))
			logTick("hedge_failed", zap.Error(err))
		}
	default:
		logTick("hold")
	}
	return nil
}

func (a *App) refreshSpotBalancesWS(ctx context.Context) {
	if a.account == nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := a.account.RefreshSpotBalancesWS(refreshCtx); err != nil {
		a.logSpotRefreshError(err)
		return
	}
	if a.spotRefreshWarned && a.log != nil {
		a.log.Info("spot balance refresh recovered")
	}
	a.spotRefreshWarned = false
}

func (a *App) logSpotRefreshError(err error) {
	if a.log == nil {
		return
	}
	if a.spotRefreshWarned {
		return
	}
	a.spotRefreshWarned = true
	a.log.Warn("spot balance refresh failed", zap.Error(err))
}

func (a *App) reconcileAccount(ctx context.Context, reason string) {
	if a.account == nil {
		return
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := a.account.Reconcile(reconcileCtx); err != nil && a.log != nil {
		a.log.Warn("account reconcile failed", zap.String("reason", reason), zap.Error(err))
	}
}

func (a *App) startMetricsServer(ctx context.Context) {
	if a.metricsServer == nil {
		return
	}
	if a.log != nil {
		a.log.Info("metrics server starting", zap.String("address", a.metricsAddr), zap.String("path", a.metricsPath))
	}
	go func() {
		if err := a.metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if a.log != nil {
				a.log.Warn("metrics server failed", zap.Error(err))
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.metricsServer.Shutdown(shutdownCtx); err != nil && a.log != nil {
			a.log.Warn("metrics server shutdown failed", zap.Error(err))
		}
	}()
}

func (a *App) startSpotReconciler(ctx context.Context) {
	if a.cfg == nil {
		return
	}
	interval := a.cfg.Strategy.SpotReconcileInterval
	if interval <= 0 {
		return
	}
	if a.log != nil {
		a.log.Info("spot reconciler started", zap.Duration("interval", interval))
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		a.refreshSpotBalancesWS(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refreshSpotBalancesWS(ctx)
			}
		}
	}()
}

func (a *App) checkConnectivity(ctx context.Context, openOrders []map[string]any, marketAge, accountAge time.Duration) error {
	if a.cfg == nil {
		return nil
	}
	err := strategy.CheckConnectivity(a.cfg.Risk, marketAge, accountAge)
	if err == nil {
		if a.killSwitchActive {
			a.killSwitchActive = false
			if a.metrics != nil {
				a.metrics.KillSwitchRestored.Inc()
			}
			if a.log != nil {
				a.log.Info("connectivity restored", zap.Duration("market_age", marketAge), zap.Duration("account_age", accountAge))
			}
		}
		return nil
	}
	if !a.killSwitchActive {
		a.killSwitchActive = true
		if a.metrics != nil {
			a.metrics.KillSwitchEngaged.Inc()
		}
		if a.log != nil {
			a.log.Warn("connectivity kill switch engaged", zap.Error(err), zap.Duration("market_age", marketAge), zap.Duration("account_age", accountAge))
		}
		if a.alerts != nil {
			if alertErr := a.alerts.Send(ctx, fmt.Sprintf("Connectivity kill switch: %v", err)); alertErr != nil && a.log != nil {
				a.log.Warn("alert send failed", zap.Error(alertErr))
			}
		}
	}
	if len(openOrders) > 0 {
		a.cancelOpenOrders(ctx, openOrders)
	}
	return err
}

func (a *App) logFundingForecastError(err error) {
	if a.log == nil {
		return
	}
	if a.fundingForecastWarned {
		return
	}
	a.fundingForecastWarned = true
	a.log.Warn("predicted funding fetch failed", zap.Error(err))
}

func (a *App) refreshFundingForecast(ctx context.Context) {
	if a.market == nil {
		return
	}
	updated, err := a.market.RefreshFundingForecast(ctx)
	if err != nil {
		a.logFundingForecastError(err)
		return
	}
	if updated {
		if a.fundingForecastWarned && a.log != nil {
			a.log.Info("predicted funding fetch recovered")
		}
		a.fundingForecastWarned = false
	}
}

func (a *App) logFundingReceiptError(err error) {
	if a.log == nil {
		return
	}
	if a.fundingReceiptWarned {
		return
	}
	a.fundingReceiptWarned = true
	a.log.Warn("funding receipt fetch failed", zap.Error(err))
}

func (a *App) maybeLogFundingReceipt(ctx context.Context, now time.Time, snap strategy.MarketSnapshot, forecast market.FundingForecast, hasForecast bool) {
	if a.account == nil || a.log == nil {
		return
	}
	if math.Abs(snap.PerpPosition) <= flatEpsilon {
		return
	}
	if !hasForecast || !forecast.HasNext || forecast.NextFunding.IsZero() {
		return
	}
	if now.Before(forecast.NextFunding.Add(fundingReceiptGrace)) {
		return
	}
	if !a.lastFundingReceiptAt.IsZero() && !a.lastFundingReceiptAt.Before(forecast.NextFunding) {
		return
	}
	if !a.lastFundingReceiptCheck.IsZero() && now.Sub(a.lastFundingReceiptCheck) < fundingReceiptCheckInterval {
		return
	}
	a.lastFundingReceiptCheck = now

	lookback := fundingReceiptLookback
	if forecast.Interval > 0 {
		intervalLookback := 2 * forecast.Interval
		if intervalLookback > lookback {
			lookback = intervalLookback
		}
	}
	start := now.Add(-lookback)
	if !a.lastFundingReceiptAt.IsZero() {
		candidate := a.lastFundingReceiptAt.Add(-fundingReceiptLookbackBuffer)
		if candidate.After(start) {
			start = candidate
		}
	}
	fundingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	entries, err := a.account.UserFunding(fundingCtx, start.UnixMilli())
	if err != nil {
		a.logFundingReceiptError(err)
		return
	}
	if a.fundingReceiptWarned && a.log != nil {
		a.log.Info("funding receipt fetch recovered")
	}
	a.fundingReceiptWarned = false

	var newest time.Time
	for _, entry := range entries {
		if entry.Asset == "" || !strings.EqualFold(entry.Asset, snap.PerpAsset) {
			continue
		}
		if entry.HasTime && !a.lastFundingReceiptAt.IsZero() && !entry.Time.After(a.lastFundingReceiptAt) {
			continue
		}
		if entry.HasTime {
			if newest.IsZero() || entry.Time.After(newest) {
				newest = entry.Time
			}
		}
		fields := []zap.Field{
			zap.String("asset", entry.Asset),
		}
		if entry.HasAmount {
			fields = append(fields, zap.Float64("amount_usdc", entry.Amount))
		}
		if entry.HasRate {
			fields = append(fields, zap.Float64("funding_rate", entry.Rate))
		}
		if entry.HasTime {
			fields = append(fields, zap.Time("funding_time", entry.Time))
		}
		fields = append(fields,
			zap.Float64("perp_position", snap.PerpPosition),
			zap.Float64("oracle_price", snap.OraclePrice),
		)
		a.log.Info("funding payment received", fields...)
	}
	if !newest.IsZero() {
		a.lastFundingReceiptAt = newest
	}
}

func (a *App) updateFundingRegime(funding, minRate, netCarryUSD, carryBufferUSD float64) (bool, bool, bool) {
	if a.cfg == nil {
		return false, false, false
	}
	ok := funding >= minRate && netCarryUSD >= carryBufferUSD
	if ok {
		a.fundingOKCount++
		a.fundingBadCount = 0
	} else {
		a.fundingBadCount++
		a.fundingOKCount = 0
	}
	okNeeded := a.cfg.Strategy.FundingConfirmations
	if okNeeded < 1 {
		okNeeded = 1
	}
	badNeeded := a.cfg.Strategy.FundingDipConfirmations
	if badNeeded < 1 {
		badNeeded = 1
	}
	return ok, a.fundingOKCount >= okNeeded, a.fundingBadCount >= badNeeded
}

func (a *App) shouldDeferExitForFunding(now time.Time, forecast market.FundingForecast, hasForecast bool, fundingRate float64) (bool, time.Duration) {
	if a.cfg == nil {
		return false, 0
	}
	if !a.exitFundingGuardEnabled() {
		return false, 0
	}
	guard := a.cfg.Strategy.ExitFundingGuard
	if guard <= 0 || !hasForecast || !forecast.HasNext || forecast.NextFunding.IsZero() {
		return false, 0
	}
	until := forecast.NextFunding.Sub(now)
	if until <= 0 {
		return false, until
	}
	if forecast.HasRate {
		if forecast.Rate <= 0 {
			return false, until
		}
	} else if fundingRate <= 0 {
		return false, until
	}
	if until > 0 && until <= guard {
		return true, until
	}
	return false, until
}

func (a *App) exitFundingGuardEnabled() bool {
	if a.cfg == nil {
		return false
	}
	if a.cfg.Strategy.ExitFundingGuardEnabled == nil {
		return true
	}
	return *a.cfg.Strategy.ExitFundingGuardEnabled
}

func (a *App) rebalanceDelta(ctx context.Context, snap strategy.MarketSnapshot) error {
	if a.cfg == nil || a.executor == nil || a.market == nil {
		return nil
	}
	band := a.cfg.Strategy.DeltaBandUSD
	if band <= 0 {
		return nil
	}
	if snap.OpenOrderCount > 0 {
		return nil
	}
	priceRef := snap.OraclePrice
	if priceRef == 0 {
		priceRef = snap.PerpMidPrice
	}
	if priceRef == 0 {
		priceRef = snap.SpotMidPrice
	}
	if priceRef == 0 {
		return errors.New("delta hedge price reference missing")
	}
	deltaBase := snap.SpotBalance + snap.PerpPosition
	deltaUSD := deltaBase * priceRef
	if math.Abs(deltaUSD) <= band {
		return nil
	}
	if math.Abs(deltaUSD) < a.cfg.Strategy.MinExposureUSD {
		return nil
	}
	perpCtx, ok := a.market.PerpContext(snap.PerpAsset)
	if !ok {
		return fmt.Errorf("perp context not found for %s", snap.PerpAsset)
	}
	size := math.Abs(deltaBase)
	if perpCtx.SzDecimals >= 0 {
		size = roundDown(size, perpCtx.SzDecimals)
	}
	if size <= 0 {
		return errors.New("delta hedge size rounded to zero")
	}
	limit := snap.PerpMidPrice
	if limit == 0 {
		limit = snap.SpotMidPrice
	}
	isBuy := deltaUSD < 0
	reduceOnly := (isBuy && snap.PerpPosition < 0) || (!isBuy && snap.PerpPosition > 0)
	limit = limitPriceWithOffset(limit, isBuy, false, perpCtx.SzDecimals, a.cfg.Strategy.IOCPriceBps)
	if limit <= 0 {
		return errors.New("delta hedge limit price invalid")
	}
	cloid, err := newCloid()
	if err != nil {
		return err
	}
	order := exec.Order{
		Asset:         perpCtx.Index,
		IsBuy:         isBuy,
		Size:          size,
		LimitPrice:    limit,
		ReduceOnly:    reduceOnly,
		ClientOrderID: cloid,
		Tif:           string(exchange.TifIoc),
	}
	if _, err := a.executor.PlaceOrder(ctx, order); err != nil {
		if a.metrics != nil {
			a.metrics.OrdersFailed.Inc()
		}
		return err
	}
	if a.metrics != nil {
		a.metrics.OrdersPlaced.Inc()
	}
	a.startHedgeCooldown(time.Now().UTC())
	if a.log != nil {
		a.log.Info("delta hedge order placed",
			zap.String("perp_asset", snap.PerpAsset),
			zap.Float64("delta_usd", deltaUSD),
			zap.Float64("band_usd", band),
			zap.Float64("size", size),
			zap.Bool("is_buy", isBuy),
			zap.Bool("reduce_only", reduceOnly),
		)
	}
	return nil
}

func (a *App) enterPosition(ctx context.Context, snap strategy.MarketSnapshot) (err error) {
	start := time.Now().UTC()
	spotCloid := ""
	perpCloid := ""
	spotLimit := 0.0
	perpLimit := 0.0
	spotRollbackLimit := 0.0
	spotSize := 0.0
	perpSize := 0.0
	spotFilled := 0.0
	perpFilled := 0.0
	defer func() {
		if err == nil {
			return
		}
		if a.metrics != nil {
			a.metrics.EntryFailed.Inc()
		}
		if a.log != nil {
			a.log.Warn("enter failed",
				zap.Error(err),
				zap.String("perp_asset", snap.PerpAsset),
				zap.String("spot_asset", snap.SpotAsset),
				zap.String("spot_cloid", spotCloid),
				zap.String("perp_cloid", perpCloid),
				zap.Duration("duration", time.Since(start)),
				zap.Float64("spot_limit", spotLimit),
				zap.Float64("perp_limit", perpLimit),
				zap.Float64("spot_size", spotSize),
				zap.Float64("perp_size", perpSize),
				zap.Float64("spot_filled", spotFilled),
				zap.Float64("perp_filled", perpFilled),
			)
		}
		if a.alerts != nil {
			if alertErr := a.alerts.Send(ctx, fmt.Sprintf("Entry failed for %s/%s: %v", snap.PerpAsset, snap.SpotAsset, err)); alertErr != nil && a.log != nil {
				a.log.Warn("alert send failed", zap.Error(alertErr))
			}
		}
	}()
	a.strategy.Apply(strategy.EventEnter)
	a.persistStrategySnapshot(ctx, snap)
	priceRef := snap.SpotMidPrice
	if snap.OraclePrice > 0 {
		priceRef = snap.OraclePrice
	}
	if priceRef == 0 {
		priceRef = snap.PerpMidPrice
	}
	size := snap.NotionalUSD / priceRef
	perpCtx, ok := a.market.PerpContext(snap.PerpAsset)
	if !ok {
		err = fmt.Errorf("perp context not found for %s", snap.PerpAsset)
		return err
	}
	perpID := perpCtx.Index
	spotCtx, err := a.spotContext(snap.SpotAsset)
	if err != nil {
		return err
	}
	spotID, ok := a.market.SpotAssetID(spotCtx.Symbol)
	if !ok {
		err = fmt.Errorf("spot asset id not found for %s", snap.SpotAsset)
		return err
	}
	spotRef := snap.SpotMidPrice
	if spotRef == 0 {
		spotRef = snap.PerpMidPrice
	}
	perpRef := snap.PerpMidPrice
	if perpRef == 0 {
		perpRef = snap.SpotMidPrice
	}
	bps := a.cfg.Strategy.IOCPriceBps
	spotLimit = limitPriceWithOffset(spotRef, true, true, spotCtx.BaseSzDecimals, bps)
	perpLimit = limitPriceWithOffset(perpRef, false, false, perpCtx.SzDecimals, bps)
	spotRollbackLimit = limitPriceWithOffset(spotRef, false, true, spotCtx.BaseSzDecimals, bps)
	spotSize = size
	if spotCtx.BaseSzDecimals >= 0 {
		spotSize = roundDown(spotSize, spotCtx.BaseSzDecimals)
	}
	if spotSize <= 0 || spotLimit <= 0 || perpLimit <= 0 {
		err = errors.New("derived order size or limit price is invalid")
		return err
	}
	spotNotional := spotSize * spotLimit
	perpNotional := spotSize * perpLimit
	if err := a.ensureEntryUSDC(ctx, spotNotional, perpNotional); err != nil {
		return err
	}
	spotCloid, err = newCloid()
	if err != nil {
		return err
	}
	perpCloid, err = newCloid()
	if err != nil {
		return err
	}
	spotOrder := exec.Order{
		Asset:         spotID,
		IsBuy:         true,
		Size:          spotSize,
		LimitPrice:    spotLimit,
		ClientOrderID: spotCloid,
		Tif:           string(exchange.TifIoc),
	}
	spotOrderID, spotFilled, spotOpen, err := a.placeAndWait(ctx, spotOrder)
	if err != nil {
		a.metrics.OrdersFailed.Inc()
		a.resetToIdle()
		return err
	}
	a.metrics.OrdersPlaced.Inc()
	if spotOpen {
		a.cancelBestEffort(ctx, spotID, spotOrderID)
	}
	if spotFilled <= 0 {
		a.resetToIdle()
		err = errors.New("spot entry did not fill")
		return err
	}

	perpSize = spotFilled
	if perpCtx.SzDecimals >= 0 {
		perpSize = roundDown(perpSize, perpCtx.SzDecimals)
	}
	if perpSize <= 0 {
		if rollbackErr := a.rollbackSpot(ctx, spotID, spotFilled, spotRollbackLimit); rollbackErr != nil {
			a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
		}
		a.resetToIdle()
		err = errors.New("perp entry size rounded to zero")
		return err
	}
	perpOrder := exec.Order{
		Asset:         perpID,
		IsBuy:         false,
		Size:          perpSize,
		LimitPrice:    perpLimit,
		ClientOrderID: perpCloid,
		Tif:           string(exchange.TifIoc),
	}
	perpOrderID, perpFilled, perpOpen, err := a.placeAndWait(ctx, perpOrder)
	if err != nil {
		a.metrics.OrdersFailed.Inc()
		if rollbackErr := a.rollbackSpot(ctx, spotID, spotFilled, spotRollbackLimit); rollbackErr != nil {
			a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
		}
		a.resetToIdle()
		return err
	}
	a.metrics.OrdersPlaced.Inc()
	if perpOpen {
		a.cancelBestEffort(ctx, perpID, perpOrderID)
	}
	if perpFilled <= 0 {
		if rollbackErr := a.rollbackSpot(ctx, spotID, spotFilled, spotRollbackLimit); rollbackErr != nil {
			a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
		}
		a.resetToIdle()
		err = errors.New("perp entry did not fill")
		return err
	}
	if residual := spotFilled - perpFilled; residual > 0 {
		if rollbackErr := a.rollbackSpot(ctx, spotID, residual, spotRollbackLimit); rollbackErr != nil {
			a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
		}
	}
	a.strategy.Apply(strategy.EventHedgeOK)
	a.persistStrategySnapshot(ctx, snap)
	a.log.Info("entered delta-neutral position",
		zap.String("perp_asset", snap.PerpAsset),
		zap.String("spot_asset", snap.SpotAsset),
		zap.String("spot_cloid", spotCloid),
		zap.String("perp_cloid", perpCloid),
		zap.Float64("spot_limit", spotLimit),
		zap.Float64("perp_limit", perpLimit),
		zap.Float64("spot_size", spotSize),
		zap.Float64("perp_size", perpSize),
		zap.Float64("spot_filled", spotFilled),
		zap.Float64("perp_filled", perpFilled),
		zap.Duration("duration", time.Since(start)),
	)
	a.startEntryCooldown(time.Now().UTC())
	a.reconcileAccount(ctx, "entry")
	if err := a.alerts.Send(ctx, fmt.Sprintf("Entered delta-neutral %s/%s size %.6f", snap.PerpAsset, snap.SpotAsset, perpFilled)); err != nil {
		a.log.Warn("alert send failed", zap.Error(err))
	}
	return nil
}

func (a *App) exitPosition(ctx context.Context, snap strategy.MarketSnapshot) (err error) {
	start := time.Now().UTC()
	spotCloid := ""
	perpCloid := ""
	spotLimit := 0.0
	perpLimit := 0.0
	spotRollbackLimit := 0.0
	spotSize := 0.0
	perpSize := 0.0
	spotFilled := 0.0
	perpFilled := 0.0
	defer func() {
		if err == nil {
			return
		}
		if a.metrics != nil {
			a.metrics.ExitFailed.Inc()
		}
		if a.log != nil {
			a.log.Warn("exit failed",
				zap.Error(err),
				zap.String("perp_asset", snap.PerpAsset),
				zap.String("spot_asset", snap.SpotAsset),
				zap.String("spot_cloid", spotCloid),
				zap.String("perp_cloid", perpCloid),
				zap.Duration("duration", time.Since(start)),
				zap.Float64("spot_limit", spotLimit),
				zap.Float64("perp_limit", perpLimit),
				zap.Float64("spot_size", spotSize),
				zap.Float64("perp_size", perpSize),
				zap.Float64("spot_filled", spotFilled),
				zap.Float64("perp_filled", perpFilled),
			)
		}
		if a.alerts != nil {
			if alertErr := a.alerts.Send(ctx, fmt.Sprintf("Exit failed for %s/%s: %v", snap.PerpAsset, snap.SpotAsset, err)); alertErr != nil && a.log != nil {
				a.log.Warn("alert send failed", zap.Error(alertErr))
			}
		}
	}()
	a.strategy.Apply(strategy.EventExit)
	a.persistStrategySnapshot(ctx, snap)
	perpCtx, ok := a.market.PerpContext(snap.PerpAsset)
	if !ok {
		err = fmt.Errorf("perp context not found for %s", snap.PerpAsset)
		return err
	}
	perpID := perpCtx.Index
	spotCtx, err := a.spotContext(snap.SpotAsset)
	if err != nil {
		return err
	}
	spotID, ok := a.market.SpotAssetID(spotCtx.Symbol)
	if !ok {
		err = fmt.Errorf("spot asset id not found for %s", snap.SpotAsset)
		return err
	}
	spotRef := snap.SpotMidPrice
	if spotRef == 0 {
		spotRef = snap.PerpMidPrice
	}
	perpRef := snap.PerpMidPrice
	if perpRef == 0 {
		perpRef = snap.SpotMidPrice
	}
	spotLimit = normalizeLimitPrice(spotRef, true, spotCtx.BaseSzDecimals)
	perpLimit = normalizeLimitPrice(perpRef, false, perpCtx.SzDecimals)
	if spotLimit <= 0 || perpLimit <= 0 {
		err = errors.New("derived order size or limit price is invalid")
		return err
	}
	spotBalance := snap.SpotBalance
	perpPosition := snap.PerpPosition
	spotRollbackLimit = limitPriceWithOffset(spotRef, spotBalance >= 0, true, spotCtx.BaseSzDecimals, a.cfg.Strategy.IOCPriceBps)
	spotSize = math.Abs(spotBalance)
	if spotCtx.BaseSzDecimals >= 0 {
		spotSize = roundDown(spotSize, spotCtx.BaseSzDecimals)
	}
	if a.exposureBelowThreshold(spotSize, spotLimit) {
		spotSize = 0
	}
	perpSize = math.Abs(perpPosition)
	if perpCtx.SzDecimals >= 0 {
		perpSize = roundDown(perpSize, perpCtx.SzDecimals)
	}
	if a.exposureBelowThreshold(perpSize, perpLimit) {
		perpSize = 0
	}
	if spotSize <= 0 && perpSize <= 0 {
		a.strategy.Apply(strategy.EventDone)
		return nil
	}
	if spotSize > 0 {
		spotCloid, err = newCloid()
		if err != nil {
			return err
		}
	}
	if perpSize > 0 {
		perpCloid, err = newCloid()
		if err != nil {
			return err
		}
	}
	if spotSize > 0 {
		spotOrder := exec.Order{
			Asset:         spotID,
			IsBuy:         spotBalance < 0,
			Size:          spotSize,
			LimitPrice:    spotLimit,
			ClientOrderID: spotCloid,
		}
		spotOrderID, filled, spotOpen, err := a.placeAndWait(ctx, spotOrder)
		if err != nil {
			return err
		}
		if spotOpen {
			a.cancelBestEffort(ctx, spotID, spotOrderID)
		}
		spotFilled = filled
		if spotFilled+flatEpsilon < spotSize {
			if spotFilled > 0 {
				if rollbackErr := a.rollbackSpotWith(ctx, spotID, spotFilled, spotRollbackLimit, spotBalance >= 0); rollbackErr != nil {
					a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
				}
			}
			a.strategy.Apply(strategy.EventHedgeOK)
			err = errors.New("spot exit did not fully fill")
			return err
		}
	}
	if perpSize > 0 {
		perpOrder := exec.Order{
			Asset:         perpID,
			IsBuy:         perpPosition < 0,
			Size:          perpSize,
			LimitPrice:    perpLimit,
			ReduceOnly:    true,
			ClientOrderID: perpCloid,
		}
		perpOrderID, perpFilled, perpOpen, err := a.placeAndWait(ctx, perpOrder)
		if err != nil {
			if spotFilled > 0 {
				if rollbackErr := a.rollbackSpotWith(ctx, spotID, spotFilled, spotRollbackLimit, spotBalance >= 0); rollbackErr != nil {
					a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
				}
			}
			a.strategy.Apply(strategy.EventHedgeOK)
			return err
		}
		if perpOpen {
			a.cancelBestEffort(ctx, perpID, perpOrderID)
		}
		if perpFilled+flatEpsilon < perpSize {
			if spotFilled > 0 {
				if rollbackErr := a.rollbackSpotWith(ctx, spotID, spotFilled, spotRollbackLimit, spotBalance >= 0); rollbackErr != nil {
					a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
				}
			}
			a.strategy.Apply(strategy.EventHedgeOK)
			err = errors.New("perp exit did not fully fill")
			return err
		}
	}
	a.strategy.Apply(strategy.EventDone)
	a.persistStrategySnapshot(ctx, snap)
	a.log.Info("exited delta-neutral position",
		zap.String("perp_asset", snap.PerpAsset),
		zap.String("spot_asset", snap.SpotAsset),
		zap.String("spot_cloid", spotCloid),
		zap.String("perp_cloid", perpCloid),
		zap.Float64("spot_limit", spotLimit),
		zap.Float64("perp_limit", perpLimit),
		zap.Float64("spot_size", spotSize),
		zap.Float64("perp_size", perpSize),
		zap.Float64("spot_filled", spotFilled),
		zap.Float64("perp_filled", perpFilled),
		zap.Duration("duration", time.Since(start)),
	)
	if err := a.alerts.Send(ctx, fmt.Sprintf("Exited delta-neutral %s/%s", snap.PerpAsset, snap.SpotAsset)); err != nil {
		a.log.Warn("alert send failed", zap.Error(err))
	}
	return nil
}

func (a *App) spotMid(ctx context.Context, asset string) (float64, market.SpotContext, error) {
	spotCtx, err := a.spotContext(asset)
	if err != nil {
		return 0, market.SpotContext{}, err
	}
	if spotCtx.MidKey != "" {
		if mid, err := a.market.Mid(ctx, spotCtx.MidKey); err == nil {
			return mid, spotCtx, nil
		}
	}
	if spotCtx.Symbol != "" {
		if mid, err := a.market.Mid(ctx, spotCtx.Symbol); err == nil {
			return mid, spotCtx, nil
		}
	}
	if asset != "" {
		if mid, err := a.market.Mid(ctx, asset); err == nil {
			return mid, spotCtx, nil
		}
	}
	return 0, spotCtx, errors.New("spot mid price not found")
}

func (a *App) spotContext(asset string) (market.SpotContext, error) {
	spotCtx, ok := a.market.SpotContext(asset)
	if !ok && !strings.Contains(asset, "/") {
		spotCtx, ok = a.market.SpotContext(asset + "/USDC")
	}
	if !ok {
		return market.SpotContext{}, fmt.Errorf("spot asset not found for %s", asset)
	}
	return spotCtx, nil
}

func (a *App) ensureSpotUSDC(ctx context.Context, required float64) error {
	if required <= 0 {
		return nil
	}
	state, err := a.account.Reconcile(ctx)
	if err != nil {
		return err
	}
	spotBalance := state.SpotBalances["USDC"]
	shortfall := required - spotBalance
	if shortfall <= 0 {
		return nil
	}
	if a.exchange == nil {
		return errors.New("exchange client is required for transfers")
	}
	if _, err := a.exchange.USDClassTransfer(ctx, shortfall, false); err != nil {
		return err
	}
	a.log.Info("transferred USDC to spot wallet", zap.Float64("amount", shortfall))
	_, err = a.account.Reconcile(ctx)
	return err
}

type usdcTransferPlan struct {
	Amount float64
	ToPerp bool
}

func planUSDCTransfer(spotUSDC, perpUSDC, spotRequired, perpRequired float64) (usdcTransferPlan, error) {
	if spotRequired < 0 {
		spotRequired = 0
	}
	if perpRequired < 0 {
		perpRequired = 0
	}
	totalRequired := spotRequired + perpRequired
	totalAvailable := spotUSDC + perpUSDC
	if totalRequired > 0 && totalAvailable+flatEpsilon < totalRequired {
		return usdcTransferPlan{}, fmt.Errorf("insufficient total USDC: need %.2f, have %.2f", totalRequired, totalAvailable)
	}
	spotShort := spotRequired - spotUSDC
	perpShort := perpRequired - perpUSDC
	if spotShort > flatEpsilon && perpShort > flatEpsilon {
		return usdcTransferPlan{}, fmt.Errorf("insufficient USDC split: need spot %.2f and perp %.2f", spotRequired, perpRequired)
	}
	if spotShort > flatEpsilon {
		return usdcTransferPlan{Amount: spotShort, ToPerp: false}, nil
	}
	if perpShort > flatEpsilon {
		return usdcTransferPlan{Amount: perpShort, ToPerp: true}, nil
	}
	return usdcTransferPlan{}, nil
}

func (a *App) ensureEntryUSDC(ctx context.Context, spotRequired, perpRequired float64) error {
	if spotRequired <= 0 && perpRequired <= 0 {
		return nil
	}
	if a.account == nil {
		return errors.New("account client is required for transfers")
	}
	state, err := a.account.Reconcile(ctx)
	if err != nil {
		return err
	}
	spotUSDC := state.SpotBalances["USDC"]
	perpUSDC := 0.0
	if state.HasMarginSummary {
		perpUSDC = state.MarginSummary.AccountValue
	}
	plan, err := planUSDCTransfer(spotUSDC, perpUSDC, spotRequired, perpRequired)
	if err != nil {
		return err
	}
	if plan.Amount <= flatEpsilon {
		return nil
	}
	if a.exchange == nil {
		return errors.New("exchange client is required for transfers")
	}
	if _, err := a.exchange.USDClassTransfer(ctx, plan.Amount, plan.ToPerp); err != nil {
		return err
	}
	if a.log != nil {
		dest := "spot"
		if plan.ToPerp {
			dest = "perp"
		}
		a.log.Info("transferred USDC to wallet", zap.String("wallet", dest), zap.Float64("amount", plan.Amount))
	}
	_, err = a.account.Reconcile(ctx)
	return err
}

func (a *App) placeAndWait(ctx context.Context, order exec.Order) (string, float64, bool, error) {
	startMS := time.Now().Add(-entryFillLookback).UnixMilli()
	orderID, err := a.executor.PlaceOrder(ctx, order)
	if err != nil {
		return "", 0, false, err
	}
	filled, open, err := a.waitForOrderFill(ctx, orderID, startMS, a.cfg.Strategy.EntryTimeout, a.cfg.Strategy.EntryPollInterval)
	return orderID, filled, open, err
}

func (a *App) waitForOrderFill(ctx context.Context, orderID string, startMS int64, timeout, poll time.Duration) (float64, bool, error) {
	if orderID == "" {
		return 0, false, errors.New("order id is required")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	lastOpen := false
	restAttempted := false
	restChecked := false
	for {
		filled, err := a.fillSizeForOrder(ctx, orderID, startMS)
		if err != nil {
			return filled, false, err
		}
		open, err := a.orderIsOpen(ctx, orderID)
		if err != nil {
			return filled, false, err
		}
		if !open && a.account != nil && a.account.FillsEnabled() && !restAttempted {
			restAttempted = true
			if restFilled, err := a.fillSizeForOrderREST(ctx, orderID, startMS); err == nil {
				restChecked = true
				if restFilled > filled {
					filled = restFilled
				}
			}
		}
		lastOpen = open
		if !open && filled > 0 {
			return filled, false, nil
		}
		select {
		case <-ctx.Done():
			return filled, false, ctx.Err()
		case <-deadline.C:
			if a.account != nil && a.account.FillsEnabled() && !restChecked {
				if restFilled, err := a.fillSizeForOrderREST(ctx, orderID, startMS); err == nil {
					restChecked = true
					if restFilled > filled {
						filled = restFilled
					}
				}
			}
			return filled, lastOpen, nil
		case <-ticker.C:
		}
	}
}

func (a *App) fillSizeForOrder(ctx context.Context, orderID string, startMS int64) (float64, error) {
	if a.account != nil && a.account.FillsEnabled() {
		return a.account.FillSize(orderID), nil
	}
	return a.fillSizeForOrderREST(ctx, orderID, startMS)
}

func (a *App) fillSizeForOrderREST(ctx context.Context, orderID string, startMS int64) (float64, error) {
	fills, err := a.account.UserFillsByTime(ctx, startMS, 0)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, fill := range fills {
		if fill.OrderID != orderID {
			continue
		}
		total += math.Abs(fill.Size)
	}
	return total, nil
}

func (a *App) orderIsOpen(ctx context.Context, orderID string) (bool, error) {
	orders, err := a.account.OpenOrders(ctx)
	if err != nil {
		return false, err
	}
	for _, id := range account.OpenOrderIDs(orders) {
		if id == orderID {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) cancelBestEffort(ctx context.Context, assetID int, orderID string) {
	if orderID == "" || assetID == 0 {
		return
	}
	if err := a.executor.CancelOrder(ctx, exec.Cancel{Asset: assetID, OrderID: orderID}); err != nil {
		a.log.Warn("failed to cancel order", zap.String("order_id", orderID), zap.Error(err))
	}
}

func (a *App) rollbackSpot(ctx context.Context, assetID int, size, limit float64) error {
	return a.rollbackSpotWith(ctx, assetID, size, limit, false)
}

func (a *App) rollbackSpotWith(ctx context.Context, assetID int, size, limit float64, isBuy bool) error {
	if size <= 0 {
		return nil
	}
	order := exec.Order{
		Asset:      assetID,
		IsBuy:      isBuy,
		Size:       size,
		LimitPrice: limit,
		Tif:        string(exchange.TifIoc),
	}
	orderID, filled, open, err := a.placeAndWait(ctx, order)
	if err != nil {
		return err
	}
	if open {
		a.cancelBestEffort(ctx, assetID, orderID)
	}
	if filled+1e-9 < size {
		return fmt.Errorf("spot rollback filled %.6f of %.6f", filled, size)
	}
	return nil
}

func (a *App) persistStrategySnapshot(ctx context.Context, snap strategy.MarketSnapshot) {
	if a.store == nil {
		return
	}
	snapshot := persist.StrategySnapshot{
		Action:       string(a.strategy.State),
		SpotAsset:    snap.SpotAsset,
		PerpAsset:    snap.PerpAsset,
		SpotMidPrice: snap.SpotMidPrice,
		PerpMidPrice: snap.PerpMidPrice,
		SpotBalance:  snap.SpotBalance,
		PerpPosition: snap.PerpPosition,
		OpenOrders:   snap.OpenOrderCount,
		UpdatedAtMS:  time.Now().UTC().UnixMilli(),
	}
	if err := persist.SaveStrategySnapshot(ctx, a.store, snapshot); err != nil {
		a.logSnapshotPersistError(err)
		return
	}
	if a.snapshotPersistWarned && a.log != nil {
		a.log.Info("strategy snapshot persistence recovered")
	}
	a.snapshotPersistWarned = false
}

func (a *App) logSnapshotPersistError(err error) {
	if a.log == nil {
		return
	}
	if a.snapshotPersistWarned {
		return
	}
	a.snapshotPersistWarned = true
	a.log.Warn("strategy snapshot persistence failed", zap.Error(err))
}

func (a *App) restoreStrategyState(accountState *account.State, restored persist.StrategySnapshot, ok bool) {
	if !ok || a.strategy == nil {
		return
	}
	state := parseStrategyState(restored.Action)
	spotBalance := 0.0
	perpPosition := 0.0
	spotPrice := restored.SpotMidPrice
	perpPrice := restored.PerpMidPrice
	if accountState != nil && a.cfg != nil {
		spotBalance = a.spotBalanceForAsset(a.cfg.Strategy.SpotAsset, accountState.SpotBalances)
		perpPosition = accountState.PerpPosition[a.cfg.Strategy.PerpAsset]
		if a.isExposureFlat(spotBalance, perpPosition, spotPrice, perpPrice) {
			state = strategy.StateIdle
		} else if state == strategy.StateIdle {
			state = strategy.StateHedgeOK
		}
	}
	a.strategy.SetState(state)
	if a.log != nil {
		a.log.Info("strategy state restored", zap.String("state", string(state)), zap.Float64("spot_balance", spotBalance), zap.Float64("perp_position", perpPosition))
	}
}

func (a *App) spotBalanceForAsset(asset string, balances map[string]float64) float64 {
	if asset == "" {
		return 0
	}
	if a.market != nil {
		if ctx, ok := a.market.SpotContext(asset); ok && ctx.Base != "" {
			return balances[ctx.Base]
		}
	}
	return balances[asset]
}

func (a *App) isExposureFlat(spotBalance, perpPosition, spotPrice, perpPrice float64) bool {
	if isFlat(spotBalance, perpPosition) {
		return true
	}
	if a.exposureBelowThreshold(spotBalance, spotPrice) && a.exposureBelowThreshold(perpPosition, perpPrice) {
		return true
	}
	return false
}

func (a *App) exposureBelowThreshold(size, price float64) bool {
	if a.cfg == nil || a.cfg.Strategy.MinExposureUSD <= 0 || price <= 0 {
		return false
	}
	return math.Abs(size)*price < a.cfg.Strategy.MinExposureUSD
}

func parseStrategyState(raw string) strategy.State {
	switch strategy.State(strings.ToUpper(strings.TrimSpace(raw))) {
	case strategy.StateEnter, strategy.StateExit, strategy.StateHedgeOK, strategy.StateIdle:
		return strategy.State(strings.ToUpper(strings.TrimSpace(raw)))
	default:
		return strategy.StateIdle
	}
}

func (a *App) resetToIdle() {
	a.strategy.Apply(strategy.EventExit)
	a.strategy.Apply(strategy.EventDone)
}

func (a *App) entryCooldownActive(now time.Time) bool {
	if a.cfg == nil {
		return false
	}
	if a.cfg.Strategy.EntryCooldown <= 0 {
		return false
	}
	return now.Before(a.entryCooldownUntil)
}

func (a *App) startEntryCooldown(now time.Time) {
	if a.cfg == nil {
		return
	}
	if a.cfg.Strategy.EntryCooldown <= 0 {
		return
	}
	a.entryCooldownUntil = now.Add(a.cfg.Strategy.EntryCooldown)
}

func (a *App) hedgeCooldownActive(now time.Time) bool {
	if a.cfg == nil {
		return false
	}
	if a.cfg.Strategy.HedgeCooldown <= 0 {
		return false
	}
	return now.Before(a.hedgeCooldownUntil)
}

func (a *App) startHedgeCooldown(now time.Time) {
	if a.cfg == nil {
		return
	}
	if a.cfg.Strategy.HedgeCooldown <= 0 {
		return
	}
	a.hedgeCooldownUntil = now.Add(a.cfg.Strategy.HedgeCooldown)
}

func isFlat(spotBalance, perpPosition float64) bool {
	return math.Abs(spotBalance) <= flatEpsilon && math.Abs(perpPosition) <= flatEpsilon
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

func applyPriceOffset(price float64, isBuy bool, bps float64) float64 {
	if price <= 0 || bps <= 0 {
		return price
	}
	scale := bps / 10000
	if isBuy {
		return price * (1 + scale)
	}
	return price * (1 - scale)
}

func limitPriceWithOffset(price float64, isBuy bool, isSpot bool, szDecimals int, bps float64) float64 {
	if price == 0 {
		return 0
	}
	price = applyPriceOffset(price, isBuy, bps)
	return normalizeLimitPrice(price, isSpot, szDecimals)
}

func newCloid() (string, error) {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UTC().UnixNano()))
	if _, err := rand.Read(b[8:]); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(b[:]), nil
}

func (a *App) cancelOpenOrders(ctx context.Context, orders []map[string]any) {
	refs := account.OpenOrderRefs(orders)
	if len(refs) == 0 {
		a.log.Warn("open orders present but no ids parsed")
		return
	}
	for _, ref := range refs {
		if ref.OrderID == "" {
			a.log.Warn("open order missing id", zap.String("asset", ref.AssetSymbol))
			continue
		}
		assetID := ref.AssetID
		if assetID == 0 && ref.AssetSymbol != "" {
			if id, ok := a.market.PerpAssetID(ref.AssetSymbol); ok {
				assetID = id
			} else if id, ok := a.market.SpotAssetID(ref.AssetSymbol); ok {
				assetID = id
			}
		}
		if assetID == 0 {
			a.log.Warn("open order missing asset id", zap.String("order_id", ref.OrderID), zap.String("asset", ref.AssetSymbol))
			continue
		}
		if err := a.executor.CancelOrder(ctx, exec.Cancel{Asset: assetID, OrderID: ref.OrderID}); err != nil {
			a.log.Warn("failed to cancel order", zap.String("order_id", ref.OrderID), zap.Error(err))
		}
	}
}

type exchangeAdapter struct {
	client *exchange.Client
	tif    exchange.Tif
	log    *zap.Logger
}

func (e *exchangeAdapter) PlaceOrder(ctx context.Context, order exec.Order) (string, error) {
	if e.client == nil {
		return "", errors.New("exchange client is required")
	}
	tif := e.tif
	if order.Tif != "" {
		tif = exchange.Tif(order.Tif)
	}
	wire, err := exchange.LimitOrderWire(order.Asset, order.IsBuy, order.Size, order.LimitPrice, order.ReduceOnly, tif, order.ClientOrderID)
	if err != nil {
		return "", err
	}
	resp, err := e.client.PlaceOrder(ctx, wire)
	if err != nil {
		return "", err
	}
	orderID := exchange.OrderIDFromResponse(resp)
	if orderID == "" {
		if e.log != nil {
			e.log.Debug("exchange response missing order id",
				zap.Any("response", resp),
				zap.Int("asset", order.Asset),
				zap.String("cloid", order.ClientOrderID),
			)
		}
		return "", errors.New("missing order id in exchange response")
	}
	return orderID, nil
}

func (e *exchangeAdapter) CancelOrder(ctx context.Context, cancel exec.Cancel) error {
	if e.client == nil {
		return errors.New("exchange client is required")
	}
	if cancel.Asset == 0 {
		return errors.New("cancel asset is required")
	}
	if cancel.OrderID == "" {
		return errors.New("cancel order id is required")
	}
	oid, err := strconv.ParseInt(cancel.OrderID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid order id %s: %w", cancel.OrderID, err)
	}
	_, err = e.client.CancelOrder(ctx, cancel.Asset, oid)
	return err
}
