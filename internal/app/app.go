package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hl-carry-bot/internal/account"
	"hl-carry-bot/internal/alerts"
	"hl-carry-bot/internal/config"
	"hl-carry-bot/internal/exec"
	"hl-carry-bot/internal/hl/rest"
	"hl-carry-bot/internal/hl/ws"
	"hl-carry-bot/internal/market"
	"hl-carry-bot/internal/metrics"
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
	market   *market.MarketData
	account  *account.Account
	executor *exec.Executor
	metrics  *metrics.Metrics
	alerts   *alerts.Telegram
	strategy *strategy.StateMachine
}

func New(cfg *config.Config, log *zap.Logger) (*App, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.State.SQLitePath), 0o755); err != nil {
		return nil, err
	}
	store, err := sqlite.New(cfg.State.SQLitePath)
	if err != nil {
		return nil, err
	}
	restClient := rest.New(cfg.REST.BaseURL, cfg.REST.Timeout, log)
	wsClient := ws.New(cfg.WS.URL, cfg.WS.ReconnectDelay, log)
	marketData := market.New(restClient, wsClient, log)
	marketData.EnableCandle(cfg.Strategy.Asset, cfg.Strategy.CandleInterval, cfg.Strategy.CandleWindow)
	accountClient := account.New(restClient)
	executor := exec.New(&restAdapter{rest: restClient}, store, log)
	metrics := metrics.NewNoop()
	alertsClient := alerts.NewTelegram(cfg.Telegram.Enabled, log)
	return &App{
		cfg:      cfg,
		log:      log,
		store:    store,
		rest:     restClient,
		ws:       wsClient,
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
	state, err := a.account.Reconcile(ctx)
	if err != nil {
		return err
	}
	a.log.Info("reconciled state",
		zap.Any("spot_balances", state.SpotBalances),
		zap.Any("perp_positions", state.PerpPosition),
		zap.Int("open_orders", len(state.OpenOrders)),
	)
	if len(state.OpenOrders) > 0 {
		a.cancelOpenOrders(ctx, state.OpenOrders)
	}
	if err := a.market.Start(ctx); err != nil {
		return err
	}
	if err := a.market.RefreshContexts(ctx); err != nil {
		a.log.Warn("context refresh failed", zap.Error(err))
	}

	ticker := time.NewTicker(a.cfg.Strategy.EntryInterval)
	defer ticker.Stop()

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
	mid, err := a.market.Mid(ctx, a.cfg.Strategy.Asset)
	if err != nil {
		return err
	}
	oraclePrice, _ := a.market.OraclePrice(a.cfg.Strategy.Asset)
	funding, _ := a.market.FundingRate(a.cfg.Strategy.Asset)
	vol, _ := a.market.Volatility(a.cfg.Strategy.Asset)

	snap := strategy.MarketSnapshot{
		Asset:          a.cfg.Strategy.Asset,
		MidPrice:       mid,
		OraclePrice:    oraclePrice,
		FundingRate:    funding,
		Volatility:     vol,
		NotionalUSD:    a.cfg.Strategy.NotionalUSD,
		OpenOrderCount: 0,
	}
	if err := strategy.CheckRisk(a.cfg.Risk, snap); err != nil {
		a.log.Warn("risk check failed", zap.Error(err))
		return nil
	}
	minExpectedFunding := snap.NotionalUSD * a.cfg.Strategy.MinFundingRate
	expectedFunding := strategy.FundingPaymentEstimateUSD(snap)

	switch a.strategy.State {
	case strategy.StateIdle:
		if expectedFunding >= minExpectedFunding && vol <= a.cfg.Strategy.MaxVolatility {
			return a.enterPosition(ctx, snap)
		}
	case strategy.StateHedgeOK:
		if a.cfg.Strategy.ExitOnFundingDip && expectedFunding < minExpectedFunding {
			return a.exitPosition(ctx, snap)
		}
	}
	return nil
}

func (a *App) enterPosition(ctx context.Context, snap strategy.MarketSnapshot) error {
	a.strategy.Apply(strategy.EventEnter)
	priceRef := snap.MidPrice
	if snap.OraclePrice > 0 {
		priceRef = snap.OraclePrice
	}
	size := snap.NotionalUSD / priceRef
	perpID, ok := a.market.PerpAssetID(snap.Asset)
	if !ok {
		return fmt.Errorf("perp asset id not found for %s", snap.Asset)
	}
	spotID, ok := a.market.SpotAssetID(snap.Asset)
	if !ok {
		return fmt.Errorf("spot asset id not found for %s", snap.Asset)
	}
	clientID := fmt.Sprintf("enter-%s", time.Now().UTC().Format("20060102T150405Z"))
	spotOrder := exec.Order{
		Asset:         spotID,
		IsBuy:         true,
		Size:          size,
		LimitPrice:    snap.MidPrice,
		ClientOrderID: clientID + "-spot",
	}
	perpOrder := exec.Order{
		Asset:         perpID,
		IsBuy:         false,
		Size:          size,
		LimitPrice:    snap.MidPrice,
		ClientOrderID: clientID + "-perp",
	}
	if _, err := a.executor.PlaceOrder(ctx, spotOrder); err != nil {
		a.metrics.OrdersFailed.Inc()
		return err
	}
	if _, err := a.executor.PlaceOrder(ctx, perpOrder); err != nil {
		a.metrics.OrdersFailed.Inc()
		return err
	}
	a.metrics.OrdersPlaced.Inc()
	a.strategy.Apply(strategy.EventHedgeOK)
	a.log.Info("entered delta-neutral position", zap.String("asset", snap.Asset), zap.Float64("size", size))
	if err := a.alerts.Send(ctx, fmt.Sprintf("Entered delta-neutral %s size %.6f", snap.Asset, size)); err != nil {
		a.log.Warn("alert send failed", zap.Error(err))
	}
	return nil
}

func (a *App) exitPosition(ctx context.Context, snap strategy.MarketSnapshot) error {
	a.strategy.Apply(strategy.EventExit)
	priceRef := snap.MidPrice
	if snap.OraclePrice > 0 {
		priceRef = snap.OraclePrice
	}
	size := snap.NotionalUSD / priceRef
	perpID, ok := a.market.PerpAssetID(snap.Asset)
	if !ok {
		return fmt.Errorf("perp asset id not found for %s", snap.Asset)
	}
	spotID, ok := a.market.SpotAssetID(snap.Asset)
	if !ok {
		return fmt.Errorf("spot asset id not found for %s", snap.Asset)
	}
	clientID := fmt.Sprintf("exit-%s", time.Now().UTC().Format("20060102T150405Z"))
	spotOrder := exec.Order{
		Asset:         spotID,
		IsBuy:         false,
		Size:          size,
		LimitPrice:    snap.MidPrice,
		ReduceOnly:    true,
		ClientOrderID: clientID + "-spot",
	}
	perpOrder := exec.Order{
		Asset:         perpID,
		IsBuy:         true,
		Size:          size,
		LimitPrice:    snap.MidPrice,
		ReduceOnly:    true,
		ClientOrderID: clientID + "-perp",
	}
	if _, err := a.executor.PlaceOrder(ctx, spotOrder); err != nil {
		return err
	}
	if _, err := a.executor.PlaceOrder(ctx, perpOrder); err != nil {
		return err
	}
	a.strategy.Apply(strategy.EventDone)
	a.log.Info("exited delta-neutral position", zap.String("asset", snap.Asset), zap.Float64("size", size))
	if err := a.alerts.Send(ctx, fmt.Sprintf("Exited delta-neutral %s size %.6f", snap.Asset, size)); err != nil {
		a.log.Warn("alert send failed", zap.Error(err))
	}
	return nil
}

func (a *App) cancelOpenOrders(ctx context.Context, orders []map[string]any) {
	orderIDs := account.OpenOrderIDs(orders)
	if len(orderIDs) == 0 {
		a.log.Warn("open orders present but no ids parsed")
		return
	}
	for _, id := range orderIDs {
		if err := a.executor.CancelOrder(ctx, id); err != nil {
			a.log.Warn("failed to cancel order", zap.String("order_id", id), zap.Error(err))
		}
	}
}

type restAdapter struct {
	rest *rest.Client
}

func (r *restAdapter) PlaceOrder(ctx context.Context, order exec.Order) (string, error) {
	return r.rest.PlaceOrder(ctx, rest.Order{
		Asset:         order.Asset,
		IsBuy:         order.IsBuy,
		Size:          order.Size,
		LimitPrice:    order.LimitPrice,
		ReduceOnly:    order.ReduceOnly,
		ClientOrderID: order.ClientOrderID,
	})
}

func (r *restAdapter) CancelOrder(ctx context.Context, orderID string) error {
	return r.rest.CancelOrder(ctx, orderID)
}
