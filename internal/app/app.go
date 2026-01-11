package app

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	cfg      *config.Config
	log      *zap.Logger
	store    *sqlite.Store
	rest     *rest.Client
	ws       *ws.Client
	exchange *exchange.Client
	market   *market.MarketData
	account  *account.Account
	executor *exec.Executor
	metrics  *metrics.Metrics
	alerts   *alerts.Telegram
	strategy *strategy.StateMachine

	snapshotPersistWarned bool
	spotRefreshWarned     bool
}

const (
	entryFillLookback = 2 * time.Second
	flatEpsilon       = 1e-6
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
	executor := exec.New(&exchangeAdapter{client: exClient, tif: exchange.TifGtc}, store, log)
	metrics := metrics.NewNoop()
	alertsClient := alerts.NewTelegram(cfg.Telegram.Enabled, log)
	return &App{
		cfg:      cfg,
		log:      log,
		store:    store,
		rest:     restClient,
		ws:       wsClient,
		exchange: exClient,
		market:   marketData,
		account:  accountClient,
		executor: executor,
		metrics:  metrics,
		alerts:   alertsClient,
		strategy: strategy.NewStateMachine(),
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	defer a.store.Close()
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
	defer a.persistStrategySnapshot(ctx, snap)
	flatStrict := isFlat(spotBalance, perpPosition)
	flat := a.isExposureFlat(spotBalance, perpPosition, spotMid, perpMid)
	spotExposureUSD := math.Abs(spotBalance) * spotMid
	perpExposureUSD := math.Abs(perpPosition) * perpMid
	minExpectedFunding := snap.NotionalUSD * a.cfg.Strategy.MinFundingRate
	expectedFunding := strategy.FundingPaymentEstimateUSD(snap)
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
			zap.Float64("funding_rate", funding),
			zap.Float64("expected_funding_usd", expectedFunding),
			zap.Float64("min_expected_funding_usd", minExpectedFunding),
			zap.Float64("volatility", vol),
			zap.Float64("max_volatility", a.cfg.Strategy.MaxVolatility),
			zap.Float64("min_exposure_usd", a.cfg.Strategy.MinExposureUSD),
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
		a.resetToIdle()
		state = a.strategy.State
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
		enterSignal := expectedFunding >= minExpectedFunding && vol <= a.cfg.Strategy.MaxVolatility
		logTick("idle", zap.Bool("enter_signal", enterSignal))
		if enterSignal {
			if a.log != nil {
				a.log.Info("enter signal", zap.Float64("expected_funding_usd", expectedFunding), zap.Float64("min_expected_funding_usd", minExpectedFunding), zap.Float64("volatility", vol), zap.Float64("max_volatility", a.cfg.Strategy.MaxVolatility))
			}
			return a.enterPosition(ctx, snap)
		}
	case strategy.StateHedgeOK:
		exitSignal := a.cfg.Strategy.ExitOnFundingDip && expectedFunding < minExpectedFunding
		logTick("hedge_ok", zap.Bool("exit_signal", exitSignal), zap.Bool("exit_on_funding_dip", a.cfg.Strategy.ExitOnFundingDip))
		if exitSignal {
			if a.log != nil {
				a.log.Info("exit signal", zap.Float64("expected_funding_usd", expectedFunding), zap.Float64("min_expected_funding_usd", minExpectedFunding))
			}
			return a.exitPosition(ctx, snap)
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

func (a *App) startSpotReconciler(ctx context.Context) {
	if a.cfg == nil {
		return
	}
	interval := a.cfg.Strategy.SpotReconcileInterval
	if interval <= 0 {
		return
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

func (a *App) enterPosition(ctx context.Context, snap strategy.MarketSnapshot) error {
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
		return fmt.Errorf("perp context not found for %s", snap.PerpAsset)
	}
	perpID := perpCtx.Index
	spotCtx, err := a.spotContext(snap.SpotAsset)
	if err != nil {
		return err
	}
	spotID, ok := a.market.SpotAssetID(spotCtx.Symbol)
	if !ok {
		return fmt.Errorf("spot asset id not found for %s", snap.SpotAsset)
	}
	spotLimit := snap.SpotMidPrice
	if spotLimit == 0 {
		spotLimit = snap.PerpMidPrice
	}
	perpLimit := snap.PerpMidPrice
	if perpLimit == 0 {
		perpLimit = snap.SpotMidPrice
	}
	spotLimit = normalizeLimitPrice(spotLimit, true, spotCtx.BaseSzDecimals)
	perpLimit = normalizeLimitPrice(perpLimit, false, perpCtx.SzDecimals)
	spotSize := size
	if spotCtx.BaseSzDecimals >= 0 {
		spotSize = roundDown(spotSize, spotCtx.BaseSzDecimals)
	}
	if spotSize <= 0 || spotLimit <= 0 || perpLimit <= 0 {
		return errors.New("derived order size or limit price is invalid")
	}
	if err := a.ensureSpotUSDC(ctx, snap.NotionalUSD); err != nil {
		return err
	}
	clientID := fmt.Sprintf("enter-%s", time.Now().UTC().Format("20060102T150405Z"))
	spotOrder := exec.Order{
		Asset:         spotID,
		IsBuy:         true,
		Size:          spotSize,
		LimitPrice:    spotLimit,
		ClientOrderID: clientID + "-spot",
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
		return errors.New("spot entry did not fill")
	}

	perpSize := spotFilled
	if perpCtx.SzDecimals >= 0 {
		perpSize = roundDown(perpSize, perpCtx.SzDecimals)
	}
	if perpSize <= 0 {
		if rollbackErr := a.rollbackSpot(ctx, spotID, spotFilled, spotLimit); rollbackErr != nil {
			a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
		}
		a.resetToIdle()
		return errors.New("perp entry size rounded to zero")
	}
	perpOrder := exec.Order{
		Asset:         perpID,
		IsBuy:         false,
		Size:          perpSize,
		LimitPrice:    perpLimit,
		ClientOrderID: clientID + "-perp",
		Tif:           string(exchange.TifIoc),
	}
	perpOrderID, perpFilled, perpOpen, err := a.placeAndWait(ctx, perpOrder)
	if err != nil {
		a.metrics.OrdersFailed.Inc()
		if rollbackErr := a.rollbackSpot(ctx, spotID, spotFilled, spotLimit); rollbackErr != nil {
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
		if rollbackErr := a.rollbackSpot(ctx, spotID, spotFilled, spotLimit); rollbackErr != nil {
			a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
		}
		a.resetToIdle()
		return errors.New("perp entry did not fill")
	}
	if residual := spotFilled - perpFilled; residual > 0 {
		if rollbackErr := a.rollbackSpot(ctx, spotID, residual, spotLimit); rollbackErr != nil {
			a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
		}
	}
	a.strategy.Apply(strategy.EventHedgeOK)
	a.persistStrategySnapshot(ctx, snap)
	a.log.Info("entered delta-neutral position", zap.String("perp_asset", snap.PerpAsset), zap.String("spot_asset", snap.SpotAsset), zap.Float64("size", perpFilled))
	if err := a.alerts.Send(ctx, fmt.Sprintf("Entered delta-neutral %s/%s size %.6f", snap.PerpAsset, snap.SpotAsset, perpFilled)); err != nil {
		a.log.Warn("alert send failed", zap.Error(err))
	}
	return nil
}

func (a *App) exitPosition(ctx context.Context, snap strategy.MarketSnapshot) error {
	a.strategy.Apply(strategy.EventExit)
	a.persistStrategySnapshot(ctx, snap)
	perpCtx, ok := a.market.PerpContext(snap.PerpAsset)
	if !ok {
		return fmt.Errorf("perp context not found for %s", snap.PerpAsset)
	}
	perpID := perpCtx.Index
	spotCtx, err := a.spotContext(snap.SpotAsset)
	if err != nil {
		return err
	}
	spotID, ok := a.market.SpotAssetID(spotCtx.Symbol)
	if !ok {
		return fmt.Errorf("spot asset id not found for %s", snap.SpotAsset)
	}
	spotLimit := snap.SpotMidPrice
	if spotLimit == 0 {
		spotLimit = snap.PerpMidPrice
	}
	perpLimit := snap.PerpMidPrice
	if perpLimit == 0 {
		perpLimit = snap.SpotMidPrice
	}
	spotLimit = normalizeLimitPrice(spotLimit, true, spotCtx.BaseSzDecimals)
	perpLimit = normalizeLimitPrice(perpLimit, false, perpCtx.SzDecimals)
	if spotLimit <= 0 || perpLimit <= 0 {
		return errors.New("derived order size or limit price is invalid")
	}
	spotBalance := snap.SpotBalance
	perpPosition := snap.PerpPosition
	spotSize := math.Abs(spotBalance)
	if spotCtx.BaseSzDecimals >= 0 {
		spotSize = roundDown(spotSize, spotCtx.BaseSzDecimals)
	}
	if a.exposureBelowThreshold(spotSize, spotLimit) {
		spotSize = 0
	}
	perpSize := math.Abs(perpPosition)
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
	clientID := fmt.Sprintf("exit-%s", time.Now().UTC().Format("20060102T150405Z"))
	spotFilled := 0.0
	if spotSize > 0 {
		spotOrder := exec.Order{
			Asset:         spotID,
			IsBuy:         spotBalance < 0,
			Size:          spotSize,
			LimitPrice:    spotLimit,
			ClientOrderID: clientID + "-spot",
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
				if rollbackErr := a.rollbackSpotWith(ctx, spotID, spotFilled, spotLimit, spotBalance >= 0); rollbackErr != nil {
					a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
				}
			}
			a.strategy.Apply(strategy.EventHedgeOK)
			return errors.New("spot exit did not fully fill")
		}
	}
	if perpSize > 0 {
		perpOrder := exec.Order{
			Asset:         perpID,
			IsBuy:         perpPosition < 0,
			Size:          perpSize,
			LimitPrice:    perpLimit,
			ReduceOnly:    true,
			ClientOrderID: clientID + "-perp",
		}
		perpOrderID, perpFilled, perpOpen, err := a.placeAndWait(ctx, perpOrder)
		if err != nil {
			if spotFilled > 0 {
				if rollbackErr := a.rollbackSpotWith(ctx, spotID, spotFilled, spotLimit, spotBalance >= 0); rollbackErr != nil {
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
				if rollbackErr := a.rollbackSpotWith(ctx, spotID, spotFilled, spotLimit, spotBalance >= 0); rollbackErr != nil {
					a.log.Warn("spot rollback failed", zap.Error(rollbackErr))
				}
			}
			a.strategy.Apply(strategy.EventHedgeOK)
			return errors.New("perp exit did not fully fill")
		}
	}
	a.strategy.Apply(strategy.EventDone)
	a.persistStrategySnapshot(ctx, snap)
	a.log.Info("exited delta-neutral position", zap.String("perp_asset", snap.PerpAsset), zap.String("spot_asset", snap.SpotAsset), zap.Float64("spot_size", spotSize), zap.Float64("perp_size", perpSize))
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

func (a *App) rollbackSpotBuy(ctx context.Context, assetID int, size, limit float64) error {
	return a.rollbackSpotWith(ctx, assetID, size, limit, true)
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
