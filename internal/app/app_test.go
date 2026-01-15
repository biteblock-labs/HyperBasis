package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
	"hl-carry-bot/internal/strategy"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"nhooyr.io/websocket"
)

func TestRoundDown(t *testing.T) {
	got := roundDown(1.239, 2)
	if math.Abs(got-1.23) > 1e-9 {
		t.Fatalf("expected 1.23, got %f", got)
	}
}

func TestNormalizeLimitPriceDecimals(t *testing.T) {
	price := normalizeLimitPrice(123.456789, true, 2)
	scaled := price * 1e6
	if math.Abs(scaled-math.Round(scaled)) > 1e-9 {
		t.Fatalf("expected spot price rounded to 6 decimals, got %f", price)
	}
	perpPrice := normalizeLimitPrice(123.456789, false, 1)
	perpScaled := perpPrice * 1e5
	if math.Abs(perpScaled-math.Round(perpScaled)) > 1e-9 {
		t.Fatalf("expected perp price rounded to 5 decimals, got %f", perpPrice)
	}
}

func TestNewCloidFormat(t *testing.T) {
	cloid, err := newCloid()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(cloid, "0x") {
		t.Fatalf("expected 0x prefix, got %s", cloid)
	}
	if len(cloid) != 34 {
		t.Fatalf("expected 34-char cloid, got %d", len(cloid))
	}
	if _, err := hex.DecodeString(cloid[2:]); err != nil {
		t.Fatalf("expected hex cloid, got %s", cloid)
	}
}

func TestEntryCooldownActive(t *testing.T) {
	cfg := &config.Config{Strategy: config.StrategyConfig{EntryCooldown: 10 * time.Second}}
	app := &App{cfg: cfg}
	now := time.Now().UTC()
	app.startEntryCooldown(now)
	if !app.entryCooldownActive(now) {
		t.Fatalf("expected entry cooldown active")
	}
	if app.entryCooldownActive(now.Add(11 * time.Second)) {
		t.Fatalf("expected entry cooldown inactive")
	}
}

func TestHedgeCooldownActive(t *testing.T) {
	cfg := &config.Config{Strategy: config.StrategyConfig{HedgeCooldown: 5 * time.Second}}
	app := &App{cfg: cfg}
	now := time.Now().UTC()
	app.startHedgeCooldown(now)
	if !app.hedgeCooldownActive(now) {
		t.Fatalf("expected hedge cooldown active")
	}
	if app.hedgeCooldownActive(now.Add(6 * time.Second)) {
		t.Fatalf("expected hedge cooldown inactive")
	}
}

func TestTickSkipsEntryDuringCooldown(t *testing.T) {
	server := newMockInfoServer(t)
	defer server.Close()
	server.nextFundingTime = time.Now().Add(1 * time.Hour).UnixMilli()

	cfg := &config.Config{
		Strategy: config.StrategyConfig{
			PerpAsset:               "ETH",
			SpotAsset:               "UETH",
			NotionalUSD:             10,
			MinFundingRate:          0,
			MaxVolatility:           1,
			FeeBps:                  0,
			SlippageBps:             0,
			CarryBufferUSD:          0,
			FundingConfirmations:    1,
			FundingDipConfirmations: 1,
			DeltaBandUSD:            5,
			MinExposureUSD:          10,
			EntryTimeout:            500 * time.Millisecond,
			EntryPollInterval:       10 * time.Millisecond,
			EntryCooldown:           1 * time.Minute,
			HedgeCooldown:           10 * time.Second,
		},
	}
	core, logs := observer.New(zap.DebugLevel)
	app := &App{
		cfg:      cfg,
		log:      zap.New(core),
		market:   newTestMarket(t, server.URL()),
		account:  newTestAccount(t, server.URL()),
		strategy: strategy.NewStateMachine(),
	}
	if _, err := app.account.Reconcile(context.Background()); err != nil {
		t.Fatalf("account reconcile: %v", err)
	}
	app.entryCooldownUntil = time.Now().Add(1 * time.Minute)

	if err := app.tick(context.Background()); err != nil {
		t.Fatalf("tick error: %v", err)
	}
	if app.strategy.State != strategy.StateIdle {
		t.Fatalf("expected idle state, got %s", app.strategy.State)
	}
	found := false
	for _, entry := range logs.FilterMessage("tick").All() {
		if entry.ContextMap()["decision"] == "skip_entry_cooldown" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected skip_entry_cooldown decision")
	}
}

func TestTickSkipsHedgeDuringCooldown(t *testing.T) {
	server := newMockInfoServer(t)
	defer server.Close()
	server.nextFundingTime = time.Now().Add(1 * time.Hour).UnixMilli()
	server.spotBalances = []any{
		map[string]any{"coin": "UETH", "total": "0.01"},
		map[string]any{"coin": "USDC", "total": "100"},
	}

	cfg := &config.Config{
		Strategy: config.StrategyConfig{
			PerpAsset:               "ETH",
			SpotAsset:               "UETH",
			NotionalUSD:             10,
			MinFundingRate:          0,
			MaxVolatility:           1,
			FeeBps:                  0,
			SlippageBps:             0,
			CarryBufferUSD:          0,
			FundingConfirmations:    1,
			FundingDipConfirmations: 1,
			DeltaBandUSD:            5,
			MinExposureUSD:          10,
			EntryTimeout:            500 * time.Millisecond,
			EntryPollInterval:       10 * time.Millisecond,
			EntryCooldown:           1 * time.Minute,
			HedgeCooldown:           1 * time.Minute,
		},
	}
	restStub := &stubRestClient{orderIDs: []string{"1"}}
	app := &App{
		cfg:      cfg,
		log:      zap.NewNop(),
		market:   newTestMarket(t, server.URL()),
		account:  newTestAccount(t, server.URL()),
		executor: exec.New(restStub, nil, zap.NewNop()),
		strategy: strategy.NewStateMachine(),
	}
	app.strategy.SetState(strategy.StateHedgeOK)
	app.hedgeCooldownUntil = time.Now().Add(1 * time.Minute)
	if _, err := app.account.Reconcile(context.Background()); err != nil {
		t.Fatalf("account reconcile: %v", err)
	}
	if err := app.tick(context.Background()); err != nil {
		t.Fatalf("tick error: %v", err)
	}
	restStub.mu.Lock()
	orderCount := len(restStub.orders)
	restStub.mu.Unlock()
	if orderCount != 0 {
		t.Fatalf("expected no hedge orders, got %d", orderCount)
	}
}

func TestTickKeepsHedgeOKDuringEntryCooldown(t *testing.T) {
	server := newMockInfoServer(t)
	defer server.Close()
	server.nextFundingTime = time.Now().Add(1 * time.Hour).UnixMilli()

	cfg := &config.Config{
		Strategy: config.StrategyConfig{
			PerpAsset:               "ETH",
			SpotAsset:               "UETH",
			NotionalUSD:             10,
			MinFundingRate:          0,
			MaxVolatility:           1,
			FeeBps:                  0,
			SlippageBps:             0,
			CarryBufferUSD:          0,
			FundingConfirmations:    1,
			FundingDipConfirmations: 1,
			DeltaBandUSD:            5,
			MinExposureUSD:          10,
			EntryTimeout:            500 * time.Millisecond,
			EntryPollInterval:       10 * time.Millisecond,
			EntryCooldown:           1 * time.Minute,
			HedgeCooldown:           1 * time.Minute,
		},
	}
	app := &App{
		cfg:      cfg,
		log:      zap.NewNop(),
		market:   newTestMarket(t, server.URL()),
		account:  newTestAccount(t, server.URL()),
		strategy: strategy.NewStateMachine(),
	}
	app.strategy.SetState(strategy.StateHedgeOK)
	app.entryCooldownUntil = time.Now().Add(1 * time.Minute)
	if _, err := app.account.Reconcile(context.Background()); err != nil {
		t.Fatalf("account reconcile: %v", err)
	}
	if err := app.tick(context.Background()); err != nil {
		t.Fatalf("tick error: %v", err)
	}
	if app.strategy.State != strategy.StateHedgeOK {
		t.Fatalf("expected hedge_ok state, got %s", app.strategy.State)
	}
}

func TestMaybeLogFundingReceiptLogsEntry(t *testing.T) {
	nextFunding := time.Unix(1700000000, 0).UTC()
	fundingTime := nextFunding.Add(100 * time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload["type"] != "userFunding" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`[
			{"delta":{"coin":"ETH","fundingRate":"0.000011","usdc":"0.002","type":"funding","szi":"-0.1"},"time":%d}
		]`, fundingTime.UnixMilli())))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	restClient := rest.New(srv.URL, 2*time.Second, zap.NewNop())
	acct := account.New(restClient, nil, zap.NewNop(), "0xabc")
	core, logs := observer.New(zap.InfoLevel)
	app := &App{
		account: acct,
		log:     zap.New(core),
	}

	now := nextFunding.Add(fundingReceiptGrace + time.Second)
	snap := strategy.MarketSnapshot{PerpAsset: "ETH", PerpPosition: -0.1, OraclePrice: 3000}
	forecast := market.FundingForecast{HasNext: true, NextFunding: nextFunding, Interval: time.Hour}

	app.maybeLogFundingReceipt(context.Background(), now, snap, forecast, true)
	entries := logs.FilterMessage("funding payment received").All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 funding log, got %d", len(entries))
	}

	app.maybeLogFundingReceipt(context.Background(), now.Add(fundingReceiptCheckInterval+time.Second), snap, forecast, true)
	entries = logs.FilterMessage("funding payment received").All()
	if len(entries) != 1 {
		t.Fatalf("expected no duplicate funding log, got %d", len(entries))
	}
}

func TestEnterPositionReconcilesAccount(t *testing.T) {
	server := newMockInfoServer(t)
	defer server.Close()
	server.nextFundingTime = time.Now().Add(1 * time.Hour).UnixMilli()
	server.spotBalances = []any{
		map[string]any{"coin": "USDC", "total": "100"},
	}
	server.accountValue = 100
	server.fills = []any{
		map[string]any{"oid": "spot-oid", "coin": "ETH", "side": "B", "sz": "0.0038", "px": "3000", "time": 1700000000000},
		map[string]any{"oid": "perp-oid", "coin": "ETH", "side": "S", "sz": "0.0038", "px": "3000", "time": 1700000000000},
	}

	cfg := &config.Config{
		Strategy: config.StrategyConfig{
			PerpAsset:         "ETH",
			SpotAsset:         "UETH",
			NotionalUSD:       10,
			MinFundingRate:    0,
			MaxVolatility:     1,
			EntryTimeout:      500 * time.Millisecond,
			EntryPollInterval: 10 * time.Millisecond,
			IOCPriceBps:       0,
			DeltaBandUSD:      5,
			MinExposureUSD:    10,
		},
	}
	restStub := &stubRestClient{orderIDs: []string{"spot-oid", "perp-oid"}}
	app := &App{
		cfg:      cfg,
		log:      zap.NewNop(),
		market:   newTestMarket(t, server.URL()),
		account:  newTestAccount(t, server.URL()),
		executor: exec.New(restStub, nil, zap.NewNop()),
		metrics:  metrics.NewNoop(),
		alerts:   alerts.NewTelegram(config.TelegramConfig{}, zap.NewNop()),
		strategy: strategy.NewStateMachine(),
	}
	if _, err := app.account.Reconcile(context.Background()); err != nil {
		t.Fatalf("account reconcile: %v", err)
	}
	initialSpot := server.Count("spotClearinghouseState")
	initialPerp := server.Count("clearinghouseState")

	snap := strategy.MarketSnapshot{
		PerpAsset:    "ETH",
		SpotAsset:    "UETH",
		SpotMidPrice: 3000,
		PerpMidPrice: 3000,
		OraclePrice:  3000,
		NotionalUSD:  10,
	}
	if err := app.enterPosition(context.Background(), snap); err != nil {
		t.Fatalf("enter position: %v", err)
	}
	if got := server.Count("spotClearinghouseState"); got != initialSpot+2 {
		t.Fatalf("expected spot reconcile after entry, got %d", got-initialSpot)
	}
	if got := server.Count("clearinghouseState"); got != initialPerp+2 {
		t.Fatalf("expected perp reconcile after entry, got %d", got-initialPerp)
	}
}

func TestPlanUSDCTransferToSpot(t *testing.T) {
	plan, err := planUSDCTransfer(5, 20, 10, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.ToPerp {
		t.Fatalf("expected transfer to spot")
	}
	if math.Abs(plan.Amount-5) > 1e-9 {
		t.Fatalf("expected amount 5, got %f", plan.Amount)
	}
}

func TestPlanUSDCTransferToPerp(t *testing.T) {
	plan, err := planUSDCTransfer(25, 2, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !plan.ToPerp {
		t.Fatalf("expected transfer to perp")
	}
	if math.Abs(plan.Amount-8) > 1e-9 {
		t.Fatalf("expected amount 8, got %f", plan.Amount)
	}
}

func TestPlanUSDCTransferInsufficientTotal(t *testing.T) {
	if _, err := planUSDCTransfer(5, 2, 10, 10); err == nil {
		t.Fatalf("expected error for insufficient total")
	}
}

func TestPlanUSDCTransferNoop(t *testing.T) {
	plan, err := planUSDCTransfer(10, 10, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Amount != 0 {
		t.Fatalf("expected zero transfer, got %f", plan.Amount)
	}
}

func TestExchangeAdapterLogsMissingOrderID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/exchange", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	signer, err := exchange.NewSigner("4f3edf983ac636a65a842ce7c78d9aa706d3b113bce036f81af8f9b72d3d80b2", true)
	if err != nil {
		t.Fatalf("signer error: %v", err)
	}
	client, err := exchange.NewClient(srv.URL, 2*time.Second, signer, "")
	if err != nil {
		t.Fatalf("client error: %v", err)
	}

	core, logs := observer.New(zap.DebugLevel)
	log := zap.New(core)
	adapter := &exchangeAdapter{client: client, tif: exchange.TifIoc, log: log}
	_, err = adapter.PlaceOrder(context.Background(), exec.Order{
		Asset:         1,
		IsBuy:         true,
		Size:          1,
		LimitPrice:    1,
		ClientOrderID: "0x00000000000000000000000000000000",
		Tif:           string(exchange.TifIoc),
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	entries := logs.FilterMessage("exchange response missing order id").All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 debug log, got %d", len(entries))
	}
	if _, ok := entries[0].ContextMap()["response"]; !ok {
		t.Fatalf("expected response field in log context")
	}
}

func TestIsFlat(t *testing.T) {
	if !isFlat(0, 0) {
		t.Fatalf("expected flat state")
	}
	if isFlat(1e-3, 0) {
		t.Fatalf("expected non-flat spot balance")
	}
	if isFlat(0, 1e-3) {
		t.Fatalf("expected non-flat perp position")
	}
}

func TestWaitForOrderFillRestFallbackRetryOnError(t *testing.T) {
	var userFillsCalls atomic.Int32
	handlerErrCh := make(chan error, 1)
	recordHandlerErr := func(err error) {
		select {
		case handlerErrCh <- err:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			recordHandlerErr(fmt.Errorf("read body: %w", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			recordHandlerErr(fmt.Errorf("decode body: %w", err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch payload["type"] {
		case "openOrders":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "userFillsByTime":
			callNum := userFillsCalls.Add(1)
			if callNum == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"temporary"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"oid":"42","coin":"BTC","side":"B","sz":"0.1","px":"30000","time":1700000000000}]`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unsupported request"}`))
		}
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	restClient := rest.New(srv.URL, 2*time.Second, zap.NewNop())
	wsURL := strings.Replace(srv.URL, "http", "ws", 1) + "/ws"
	wsClient := ws.New(wsURL, 10*time.Millisecond, 0, zap.NewNop())
	acct := account.New(restClient, wsClient, zap.NewNop(), "0xabc")
	if err := acct.Start(ctx); err != nil {
		t.Fatalf("account start: %v", err)
	}
	if !acct.FillsEnabled() {
		t.Fatalf("expected fills enabled")
	}

	app := &App{account: acct}
	startMS := time.Now().Add(-time.Second).UnixMilli()
	filled, open, err := app.waitForOrderFill(ctx, "42", startMS, 80*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForOrderFill: %v", err)
	}
	if open {
		t.Fatalf("expected open=false, got true")
	}
	if math.Abs(filled-0.1) > 1e-9 {
		t.Fatalf("expected filled=0.1, got %f", filled)
	}
	if got := userFillsCalls.Load(); got != 2 {
		t.Fatalf("expected 2 userFillsByTime calls, got %d", got)
	}
	select {
	case err := <-handlerErrCh:
		t.Fatalf("handler error: %v", err)
	default:
	}
}

func TestRestoreStrategyStateKeepsActionWhenExposed(t *testing.T) {
	app := &App{
		cfg:      &config.Config{Strategy: config.StrategyConfig{PerpAsset: "BTC", SpotAsset: "UBTC"}},
		strategy: strategy.NewStateMachine(),
		log:      zap.NewNop(),
	}
	accountState := &account.State{
		SpotBalances: map[string]float64{"UBTC": 1},
		PerpPosition: map[string]float64{"BTC": -1},
	}
	snapshot := persist.StrategySnapshot{Action: "EXIT"}
	app.restoreStrategyState(accountState, snapshot, true)
	if app.strategy.State != strategy.StateExit {
		t.Fatalf("expected %s, got %s", strategy.StateExit, app.strategy.State)
	}
}

func TestRestoreStrategyStatePromotesIdleWhenExposed(t *testing.T) {
	app := &App{
		cfg:      &config.Config{Strategy: config.StrategyConfig{PerpAsset: "BTC", SpotAsset: "UBTC"}},
		strategy: strategy.NewStateMachine(),
		log:      zap.NewNop(),
	}
	accountState := &account.State{
		SpotBalances: map[string]float64{"UBTC": 1},
		PerpPosition: map[string]float64{"BTC": -1},
	}
	snapshot := persist.StrategySnapshot{Action: "IDLE"}
	app.restoreStrategyState(accountState, snapshot, true)
	if app.strategy.State != strategy.StateHedgeOK {
		t.Fatalf("expected %s, got %s", strategy.StateHedgeOK, app.strategy.State)
	}
}

func TestRestoreStrategyStateFlatResetsIdle(t *testing.T) {
	app := &App{
		cfg:      &config.Config{Strategy: config.StrategyConfig{PerpAsset: "BTC", SpotAsset: "UBTC"}},
		strategy: strategy.NewStateMachine(),
		log:      zap.NewNop(),
	}
	accountState := &account.State{
		SpotBalances: map[string]float64{"UBTC": 0},
		PerpPosition: map[string]float64{"BTC": 0},
	}
	snapshot := persist.StrategySnapshot{Action: "EXIT"}
	app.restoreStrategyState(accountState, snapshot, true)
	if app.strategy.State != strategy.StateIdle {
		t.Fatalf("expected %s, got %s", strategy.StateIdle, app.strategy.State)
	}
}

func TestExitPositionRollsBackOnPerpNoFill(t *testing.T) {
	fills := map[string]float64{
		"spot-1":     1,
		"rollback-1": 1,
	}
	info := &fillServer{fills: fills}
	srv := httptest.NewServer(http.HandlerFunc(info.handle))
	defer srv.Close()

	marketData := newTestMarket(t, srv.URL)
	accountClient := newTestAccount(t, srv.URL)
	stub := &stubRestClient{orderIDs: []string{"spot-1", "perp-1", "rollback-1"}}
	metricsStub, counters := newTestMetrics()
	app := &App{
		cfg: &config.Config{Strategy: config.StrategyConfig{
			EntryTimeout:      30 * time.Millisecond,
			EntryPollInterval: 5 * time.Millisecond,
		}},
		log:      zap.NewNop(),
		market:   marketData,
		account:  accountClient,
		executor: exec.New(stub, nil, zap.NewNop()),
		metrics:  metricsStub,
		alerts:   alerts.NewTelegram(config.TelegramConfig{Enabled: false}, zap.NewNop()),
		strategy: strategy.NewStateMachine(),
	}
	app.strategy.Apply(strategy.EventEnter)
	app.strategy.Apply(strategy.EventHedgeOK)

	snap := strategy.MarketSnapshot{
		PerpAsset:    "BTC",
		SpotAsset:    "UBTC",
		NotionalUSD:  100,
		SpotMidPrice: 100,
		PerpMidPrice: 100,
		SpotBalance:  1,
		PerpPosition: -1,
	}
	err := app.exitPosition(context.Background(), snap)
	if err == nil {
		t.Fatalf("expected error on perp exit no fill")
	}
	if counters.exitFailed.count != 1 {
		t.Fatalf("expected exit failed count 1, got %d", counters.exitFailed.count)
	}
	if app.strategy.State != strategy.StateHedgeOK {
		t.Fatalf("expected strategy to return to hedge ok, got %s", app.strategy.State)
	}
	if got := len(stub.orders); got != 3 {
		t.Fatalf("expected 3 orders (spot, perp, rollback), got %d", got)
	}
	if stub.orders[0].IsBuy || stub.orders[0].Asset != 10000 {
		t.Fatalf("expected first order to be spot sell, got %+v", stub.orders[0])
	}
	if !stub.orders[1].IsBuy || !stub.orders[1].ReduceOnly {
		t.Fatalf("expected second order to be reduce-only perp buy, got %+v", stub.orders[1])
	}
	if !stub.orders[2].IsBuy || stub.orders[2].Asset != 10000 {
		t.Fatalf("expected rollback spot buy, got %+v", stub.orders[2])
	}
}

func TestExitPositionSuccess(t *testing.T) {
	fills := map[string]float64{
		"spot-1": 1,
		"perp-1": 1,
	}
	info := &fillServer{fills: fills}
	srv := httptest.NewServer(http.HandlerFunc(info.handle))
	defer srv.Close()

	marketData := newTestMarket(t, srv.URL)
	accountClient := newTestAccount(t, srv.URL)
	stub := &stubRestClient{orderIDs: []string{"spot-1", "perp-1"}}
	app := &App{
		cfg: &config.Config{Strategy: config.StrategyConfig{
			EntryTimeout:      30 * time.Millisecond,
			EntryPollInterval: 5 * time.Millisecond,
		}},
		log:      zap.NewNop(),
		market:   marketData,
		account:  accountClient,
		executor: exec.New(stub, nil, zap.NewNop()),
		metrics:  metrics.NewNoop(),
		alerts:   alerts.NewTelegram(config.TelegramConfig{Enabled: false}, zap.NewNop()),
		strategy: strategy.NewStateMachine(),
	}
	app.strategy.Apply(strategy.EventEnter)
	app.strategy.Apply(strategy.EventHedgeOK)

	snap := strategy.MarketSnapshot{
		PerpAsset:    "BTC",
		SpotAsset:    "UBTC",
		NotionalUSD:  100,
		SpotMidPrice: 100,
		PerpMidPrice: 100,
		SpotBalance:  1,
		PerpPosition: -1,
	}
	if err := app.exitPosition(context.Background(), snap); err != nil {
		t.Fatalf("expected exit success, got %v", err)
	}
	if app.strategy.State != strategy.StateIdle {
		t.Fatalf("expected strategy idle, got %s", app.strategy.State)
	}
	if got := len(stub.orders); got != 2 {
		t.Fatalf("expected 2 orders (spot, perp), got %d", got)
	}
}

func TestEnterPositionFailureIncrementsMetric(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch payload["type"] {
		case "metaAndAssetCtxs":
			writeJSON(w, perpCtxPayload())
		case "spotMetaAndAssetCtxs":
			writeJSON(w, spotCtxPayload())
		default:
			writeJSON(w, []any{})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	marketData := newTestMarket(t, srv.URL)
	metricsStub, counters := newTestMetrics()
	app := &App{
		cfg: &config.Config{Strategy: config.StrategyConfig{
			EntryTimeout:      30 * time.Millisecond,
			EntryPollInterval: 5 * time.Millisecond,
		}},
		log:      zap.NewNop(),
		market:   marketData,
		metrics:  metricsStub,
		alerts:   alerts.NewTelegram(config.TelegramConfig{Enabled: false}, zap.NewNop()),
		strategy: strategy.NewStateMachine(),
	}

	snap := strategy.MarketSnapshot{
		PerpAsset:    "BTC",
		SpotAsset:    "UBTC",
		NotionalUSD:  100,
		SpotMidPrice: 0,
		PerpMidPrice: 0,
	}
	err := app.enterPosition(context.Background(), snap)
	if err == nil {
		t.Fatalf("expected entry error")
	}
	if counters.entryFailed.count != 1 {
		t.Fatalf("expected entry failed count 1, got %d", counters.entryFailed.count)
	}
}

func TestRebalanceDeltaPlacesPerpOrder(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch payload["type"] {
		case "metaAndAssetCtxs":
			writeJSON(w, perpCtxPayload())
		case "spotMetaAndAssetCtxs":
			writeJSON(w, spotCtxPayload())
		default:
			writeJSON(w, []any{})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	marketData := newTestMarket(t, srv.URL)
	stub := &stubRestClient{orderIDs: []string{"hedge-1"}}
	app := &App{
		cfg: &config.Config{Strategy: config.StrategyConfig{
			DeltaBandUSD:   20,
			MinExposureUSD: 10,
			IOCPriceBps:    10,
		}},
		log:      zap.NewNop(),
		market:   marketData,
		executor: exec.New(stub, nil, zap.NewNop()),
		metrics:  metrics.NewNoop(),
	}
	snap := strategy.MarketSnapshot{
		PerpAsset:    "BTC",
		SpotAsset:    "UBTC",
		SpotMidPrice: 100,
		PerpMidPrice: 100,
		SpotBalance:  1,
		PerpPosition: -0.4,
	}
	if err := app.rebalanceDelta(context.Background(), snap); err != nil {
		t.Fatalf("rebalance delta: %v", err)
	}
	if got := len(stub.orders); got != 1 {
		t.Fatalf("expected 1 hedge order, got %d", got)
	}
	order := stub.orders[0]
	if order.Asset != 0 {
		t.Fatalf("expected perp asset 0, got %d", order.Asset)
	}
	if order.IsBuy {
		t.Fatalf("expected sell order, got buy")
	}
	if order.ReduceOnly {
		t.Fatalf("expected reduce-only=false, got true")
	}
	if math.Abs(order.Size-0.6) > 1e-9 {
		t.Fatalf("expected size 0.6, got %f", order.Size)
	}
	if math.Abs(order.LimitPrice-99.9) > 1e-9 {
		t.Fatalf("expected limit 99.9, got %f", order.LimitPrice)
	}
	if !strings.HasPrefix(order.ClientOrderID, "0x") {
		t.Fatalf("expected hex cloid, got %s", order.ClientOrderID)
	}
	if len(order.ClientOrderID) != 34 {
		t.Fatalf("expected 34-char cloid, got %d", len(order.ClientOrderID))
	}
	if _, err := hex.DecodeString(order.ClientOrderID[2:]); err != nil {
		t.Fatalf("expected hex cloid, got %s", order.ClientOrderID)
	}
}

func TestRebalanceDeltaSkipsWithinBand(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch payload["type"] {
		case "metaAndAssetCtxs":
			writeJSON(w, perpCtxPayload())
		case "spotMetaAndAssetCtxs":
			writeJSON(w, spotCtxPayload())
		default:
			writeJSON(w, []any{})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	marketData := newTestMarket(t, srv.URL)
	stub := &stubRestClient{orderIDs: []string{"hedge-1"}}
	app := &App{
		cfg: &config.Config{Strategy: config.StrategyConfig{
			DeltaBandUSD:   100,
			MinExposureUSD: 10,
		}},
		log:      zap.NewNop(),
		market:   marketData,
		executor: exec.New(stub, nil, zap.NewNop()),
		metrics:  metrics.NewNoop(),
	}
	snap := strategy.MarketSnapshot{
		PerpAsset:    "BTC",
		SpotAsset:    "UBTC",
		SpotMidPrice: 100,
		PerpMidPrice: 100,
		SpotBalance:  1,
		PerpPosition: -0.4,
	}
	if err := app.rebalanceDelta(context.Background(), snap); err != nil {
		t.Fatalf("rebalance delta: %v", err)
	}
	if got := len(stub.orders); got != 0 {
		t.Fatalf("expected no hedge orders, got %d", got)
	}
}

func TestConnectivityKillSwitchRetriesCancel(t *testing.T) {
	stub := &stubRestClient{}
	metricsStub, counters := newTestMetrics()
	app := &App{
		cfg:      &config.Config{Risk: config.RiskConfig{MaxMarketAge: time.Second}},
		log:      zap.NewNop(),
		executor: exec.New(stub, nil, zap.NewNop()),
		metrics:  metricsStub,
	}
	openOrders := []map[string]any{{"oid": "1", "asset": 1}}
	if err := app.checkConnectivity(context.Background(), app.riskConfig(), openOrders, 2*time.Second, 0); err == nil {
		t.Fatalf("expected connectivity error")
	}
	if !app.killSwitchActive {
		t.Fatalf("expected kill switch active")
	}
	if got := len(stub.cancels); got != 1 {
		t.Fatalf("expected 1 cancel attempt, got %d", got)
	}
	if counters.killEngaged.count != 1 {
		t.Fatalf("expected kill switch engaged count 1, got %d", counters.killEngaged.count)
	}
	if err := app.checkConnectivity(context.Background(), app.riskConfig(), openOrders, 2*time.Second, 0); err == nil {
		t.Fatalf("expected connectivity error on retry")
	}
	if got := len(stub.cancels); got != 2 {
		t.Fatalf("expected 2 cancel attempts, got %d", got)
	}
	if counters.killEngaged.count != 1 {
		t.Fatalf("expected kill switch engaged count to remain 1, got %d", counters.killEngaged.count)
	}
}

func TestConnectivityKillSwitchRestores(t *testing.T) {
	stub := &stubRestClient{}
	metricsStub, counters := newTestMetrics()
	app := &App{
		cfg:      &config.Config{Risk: config.RiskConfig{MaxMarketAge: time.Second}},
		log:      zap.NewNop(),
		executor: exec.New(stub, nil, zap.NewNop()),
		metrics:  metricsStub,
	}
	openOrders := []map[string]any{{"oid": "1", "asset": 1}}
	_ = app.checkConnectivity(context.Background(), app.riskConfig(), openOrders, 2*time.Second, 0)
	if !app.killSwitchActive {
		t.Fatalf("expected kill switch active")
	}
	if err := app.checkConnectivity(context.Background(), app.riskConfig(), openOrders, 0, 0); err != nil {
		t.Fatalf("expected connectivity restored, got %v", err)
	}
	if app.killSwitchActive {
		t.Fatalf("expected kill switch cleared")
	}
	if counters.killEngaged.count != 1 {
		t.Fatalf("expected kill switch engaged count 1, got %d", counters.killEngaged.count)
	}
	if counters.killRestored.count != 1 {
		t.Fatalf("expected kill switch restored count 1, got %d", counters.killRestored.count)
	}
}

func TestFundingRegimeConfirmations(t *testing.T) {
	app := &App{
		cfg: &config.Config{Strategy: config.StrategyConfig{
			MinFundingRate:          0.01,
			CarryBufferUSD:          1,
			FundingConfirmations:    2,
			FundingDipConfirmations: 2,
		}},
	}
	_, okConfirmed, badConfirmed := app.updateFundingRegime(0.01, 0.01, 2, 1)
	if okConfirmed {
		t.Fatalf("expected funding ok not yet confirmed")
	}
	if badConfirmed {
		t.Fatalf("expected funding bad not confirmed")
	}
	_, okConfirmed, _ = app.updateFundingRegime(0.01, 0.01, 2, 1)
	if !okConfirmed {
		t.Fatalf("expected funding ok confirmed")
	}
	_, okConfirmed, badConfirmed = app.updateFundingRegime(0.0, 0.01, 0.5, 1)
	if okConfirmed {
		t.Fatalf("expected funding ok reset on dip")
	}
	if badConfirmed {
		t.Fatalf("expected funding dip not yet confirmed")
	}
	_, _, badConfirmed = app.updateFundingRegime(0.0, 0.01, 0.5, 1)
	if !badConfirmed {
		t.Fatalf("expected funding dip confirmed")
	}
}

func TestFundingForecastWarningResetsOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload["type"] != "predictedFundings" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, []any{
			[]any{"BTC", []any{
				[]any{"HlPerp", map[string]any{"fundingRate": "0.001", "nextFundingTime": 1700000000000}},
			}},
		})
	}))
	defer server.Close()

	restClient := rest.New(server.URL, 2*time.Second, zap.NewNop())
	marketData := market.New(restClient, nil, zap.NewNop())
	app := &App{market: marketData, log: zap.NewNop()}
	app.fundingForecastWarned = true
	app.refreshFundingForecast(context.Background())
	if app.fundingForecastWarned {
		t.Fatalf("expected funding forecast warning reset after success")
	}
}

func TestShouldDeferExitForFunding(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	app := &App{cfg: &config.Config{Strategy: config.StrategyConfig{
		ExitFundingGuard: 2 * time.Minute,
	}}}
	forecast := market.FundingForecast{
		NextFunding: now.Add(90 * time.Second),
		HasNext:     true,
		HasRate:     true,
		Rate:        0.0001,
	}
	guarded, until := app.shouldDeferExitForFunding(now, forecast, true, 0.0001)
	if !guarded {
		t.Fatalf("expected exit to be guarded")
	}
	if until != 90*time.Second {
		t.Fatalf("expected time-to-funding 90s, got %s", until)
	}

	forecast.NextFunding = now.Add(5 * time.Minute)
	guarded, _ = app.shouldDeferExitForFunding(now, forecast, true, 0.0001)
	if guarded {
		t.Fatalf("expected exit not guarded when outside window")
	}
}

func TestShouldDeferExitForFundingNegativeRate(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	app := &App{cfg: &config.Config{Strategy: config.StrategyConfig{
		ExitFundingGuard: 2 * time.Minute,
	}}}
	forecast := market.FundingForecast{
		NextFunding: now.Add(30 * time.Second),
		HasNext:     true,
		HasRate:     true,
		Rate:        -0.0001,
	}
	guarded, _ := app.shouldDeferExitForFunding(now, forecast, true, -0.0001)
	if guarded {
		t.Fatalf("expected exit not guarded for negative funding rate")
	}
}

func TestShouldDeferExitForFundingDisabled(t *testing.T) {
	enabled := false
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	app := &App{cfg: &config.Config{Strategy: config.StrategyConfig{
		ExitFundingGuard:        2 * time.Minute,
		ExitFundingGuardEnabled: &enabled,
	}}}
	forecast := market.FundingForecast{
		NextFunding: now.Add(30 * time.Second),
		HasNext:     true,
		HasRate:     true,
		Rate:        0.0001,
	}
	guarded, _ := app.shouldDeferExitForFunding(now, forecast, true, 0.0001)
	if guarded {
		t.Fatalf("expected exit not guarded when guard disabled")
	}
}

type testCounter struct {
	count int
}

func (c *testCounter) Inc() {
	c.count++
}

type metricsCounters struct {
	ordersPlaced *testCounter
	ordersFailed *testCounter
	entryFailed  *testCounter
	exitFailed   *testCounter
	killEngaged  *testCounter
	killRestored *testCounter
}

func newTestMetrics() (*metrics.Metrics, *metricsCounters) {
	counters := &metricsCounters{
		ordersPlaced: &testCounter{},
		ordersFailed: &testCounter{},
		entryFailed:  &testCounter{},
		exitFailed:   &testCounter{},
		killEngaged:  &testCounter{},
		killRestored: &testCounter{},
	}
	m := &metrics.Metrics{
		OrdersPlaced:       counters.ordersPlaced,
		OrdersFailed:       counters.ordersFailed,
		EntryFailed:        counters.entryFailed,
		ExitFailed:         counters.exitFailed,
		KillSwitchEngaged:  counters.killEngaged,
		KillSwitchRestored: counters.killRestored,
	}
	return m, counters
}

type fillServer struct {
	mu    sync.RWMutex
	fills map[string]float64
}

func (s *fillServer) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch payload["type"] {
	case "metaAndAssetCtxs":
		writeJSON(w, perpCtxPayload())
	case "spotMetaAndAssetCtxs":
		writeJSON(w, spotCtxPayload())
	case "openOrders":
		writeJSON(w, []any{})
	case "userFillsByTime":
		s.mu.RLock()
		fills := make([]map[string]any, 0, len(s.fills))
		for oid, size := range s.fills {
			if size == 0 {
				continue
			}
			fills = append(fills, map[string]any{
				"oid":  oid,
				"coin": "BTC",
				"side": "B",
				"sz":   size,
				"px":   "100",
				"time": 1700000000000,
			})
		}
		s.mu.RUnlock()
		writeJSON(w, fills)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unsupported request"}`))
	}
}

func perpCtxPayload() []any {
	return []any{
		map[string]any{
			"universe": []any{
				map[string]any{"name": "BTC", "index": 0, "szDecimals": 3},
			},
		},
		[]any{
			map[string]any{"funding": 0, "oraclePx": 100, "markPx": 100},
		},
	}
}

func spotCtxPayload() []any {
	return []any{
		map[string]any{
			"universe": []any{
				map[string]any{"name": "UBTC/USDC", "index": 0, "tokens": []any{0, 1}},
			},
			"tokens": []any{
				map[string]any{"name": "UBTC", "index": 0, "szDecimals": 3},
				map[string]any{"name": "USDC", "index": 1, "szDecimals": 2},
			},
		},
	}
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

type stubRestClient struct {
	mu       sync.Mutex
	orderIDs []string
	orders   []exec.Order
	cancels  []exec.Cancel
}

func (s *stubRestClient) PlaceOrder(ctx context.Context, order exec.Order) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders = append(s.orders, order)
	if len(s.orderIDs) == 0 {
		return "", errors.New("no order ids available")
	}
	orderID := s.orderIDs[0]
	s.orderIDs = s.orderIDs[1:]
	return orderID, nil
}

func (s *stubRestClient) CancelOrder(ctx context.Context, cancel exec.Cancel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels = append(s.cancels, cancel)
	return nil
}

type mockInfoServer struct {
	t *testing.T

	mu              sync.Mutex
	counts          map[string]int
	spotBalances    []any
	accountValue    float64
	mids            map[string]any
	fundingRate     string
	nextFundingTime int64
	fills           []any
	server          *httptest.Server
}

func newMockInfoServer(t *testing.T) *mockInfoServer {
	t.Helper()
	m := &mockInfoServer{
		t:            t,
		counts:       make(map[string]int),
		accountValue: 100,
		fundingRate:  "0.00001",
		mids: map[string]any{
			"ETH":       "3000",
			"UETH/USDC": "3000",
		},
		spotBalances: []any{
			map[string]any{"coin": "USDC", "total": "100"},
		},
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockInfoServer) Close() {
	m.server.Close()
}

func (m *mockInfoServer) URL() string {
	return m.server.URL
}

func (m *mockInfoServer) Count(typ string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[typ]
}

func (m *mockInfoServer) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	typ, _ := payload["type"].(string)
	if typ == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.counts[typ]++
	spotBalances := m.spotBalances
	accountValue := m.accountValue
	mids := m.mids
	fundingRate := m.fundingRate
	nextFundingTime := m.nextFundingTime
	fills := m.fills
	m.mu.Unlock()

	switch typ {
	case "metaAndAssetCtxs":
		writeJSON(w, []any{
			map[string]any{"universe": []any{
				map[string]any{"name": "ETH", "szDecimals": 3, "index": 1},
			}},
			[]any{
				map[string]any{"funding": fundingRate, "oraclePx": "3000", "markPx": "3000"},
			},
		})
	case "spotMetaAndAssetCtxs", "spotMeta":
		writeJSON(w, []any{
			map[string]any{"universe": []any{
				map[string]any{"name": "UETH/USDC", "index": 51, "base": "UETH", "quote": "USDC", "szDecimals": 3},
			}},
		})
	case "allMids":
		writeJSON(w, mids)
	case "predictedFundings":
		writeJSON(w, []any{
			[]any{"ETH", []any{
				[]any{"HlPerp", map[string]any{
					"fundingRate":          fundingRate,
					"nextFundingTime":      nextFundingTime,
					"fundingIntervalHours": 1,
				}},
			}},
		})
	case "spotClearinghouseState":
		writeJSON(w, map[string]any{"balances": spotBalances})
	case "clearinghouseState":
		writeJSON(w, map[string]any{
			"assetPositions": []any{},
			"marginSummary":  map[string]any{"accountValue": accountValue},
		})
	case "openOrders":
		writeJSON(w, []any{})
	case "userFillsByTime":
		if fills == nil {
			fills = []any{}
		}
		writeJSON(w, fills)
	case "userFunding":
		writeJSON(w, []any{})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func newTestMarket(t *testing.T, baseURL string) *market.MarketData {
	t.Helper()
	restClient := rest.New(baseURL, 2*time.Second, zap.NewNop())
	wsClient := ws.New("ws://unused", 10*time.Millisecond, 0, zap.NewNop())
	marketData := market.New(restClient, wsClient, zap.NewNop())
	if err := marketData.RefreshContexts(context.Background()); err != nil {
		t.Fatalf("market refresh: %v", err)
	}
	return marketData
}

func newTestAccount(t *testing.T, baseURL string) *account.Account {
	t.Helper()
	restClient := rest.New(baseURL, 2*time.Second, zap.NewNop())
	return account.New(restClient, nil, zap.NewNop(), "0xabc")
}
