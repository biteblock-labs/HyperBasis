package app

import (
	"context"
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
	"hl-carry-bot/internal/hl/rest"
	"hl-carry-bot/internal/hl/ws"
	"hl-carry-bot/internal/market"
	"hl-carry-bot/internal/metrics"
	persist "hl-carry-bot/internal/state"
	"hl-carry-bot/internal/strategy"

	"go.uber.org/zap"
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
		alerts:   alerts.NewTelegram(false, zap.NewNop()),
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
		alerts:   alerts.NewTelegram(false, zap.NewNop()),
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
