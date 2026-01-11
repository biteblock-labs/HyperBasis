package market

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"hl-carry-bot/internal/hl/rest"
	"hl-carry-bot/internal/hl/ws"

	"go.uber.org/zap"
)

type PerpContext struct {
	Index       int
	FundingRate float64
	OraclePrice float64
	MarkPrice   float64
	SzDecimals  int
}

type SpotContext struct {
	Symbol          string
	Base            string
	Quote           string
	Index           int
	BaseSzDecimals  int
	QuoteSzDecimals int
	RawName         string
	MidKey          string
}

type MarketData struct {
	rest *rest.Client
	ws   *ws.Client
	log  *zap.Logger

	mu               sync.RWMutex
	midPrices        map[string]float64
	funding          map[string]float64
	oraclePrices     map[string]float64
	volatility       map[string]float64
	perpCtx          map[string]PerpContext
	spotCtx          map[string]SpotContext
	candleCloses     map[string][]float64
	lastCtxRefresh   time.Time
	ctxRefreshWindow time.Duration

	candleAsset    string
	candleInterval string
	candleWindow   int
}

func New(restClient *rest.Client, wsClient *ws.Client, log *zap.Logger) *MarketData {
	return &MarketData{
		rest:             restClient,
		ws:               wsClient,
		log:              log,
		midPrices:        make(map[string]float64),
		funding:          make(map[string]float64),
		oraclePrices:     make(map[string]float64),
		volatility:       make(map[string]float64),
		perpCtx:          make(map[string]PerpContext),
		spotCtx:          make(map[string]SpotContext),
		candleCloses:     make(map[string][]float64),
		ctxRefreshWindow: 30 * time.Second,
		candleWindow:     20,
		candleInterval:   "1h",
	}
}

func (m *MarketData) EnableCandle(asset, interval string, window int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.candleAsset = asset
	if interval != "" {
		m.candleInterval = interval
	}
	if window > 0 {
		m.candleWindow = window
	}
}

func (m *MarketData) Start(ctx context.Context) error {
	if m.ws == nil {
		return nil
	}
	sub := map[string]any{"method": "subscribe", "subscription": map[string]any{"type": "allMids"}}
	if err := m.ws.Connect(ctx); err != nil {
		return err
	}
	if err := m.ws.Subscribe(ctx, sub); err != nil {
		return err
	}
	m.subscribeCandle(ctx)
	if err := m.RefreshContexts(ctx); err != nil {
		m.log.Warn("context refresh failed", zap.Error(err))
	}
	go func() {
		_ = m.ws.Run(ctx, m.handleMessage)
	}()
	return nil
}

func (m *MarketData) subscribeCandle(ctx context.Context) {
	m.mu.RLock()
	asset := m.candleAsset
	interval := m.candleInterval
	m.mu.RUnlock()
	if asset == "" {
		return
	}
	sub := map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type":     "candle",
			"coin":     asset,
			"interval": interval,
		},
	}
	if err := m.ws.Subscribe(ctx, sub); err != nil {
		m.log.Warn("candle subscribe failed", zap.Error(err))
	}
}

func (m *MarketData) RefreshContexts(ctx context.Context) error {
	if m.rest == nil {
		return nil
	}
	if !m.shouldRefresh() {
		return nil
	}
	perpResp, err := m.rest.InfoAny(ctx, rest.InfoRequest{Type: "metaAndAssetCtxs"})
	if err != nil {
		return err
	}
	spotResp, err := m.rest.InfoAny(ctx, rest.InfoRequest{Type: "spotMetaAndAssetCtxs"})
	if err != nil {
		spotResp, err = m.rest.InfoAny(ctx, rest.InfoRequest{Type: "spotMeta"})
		if err != nil {
			return err
		}
	}
	perpCtx, err := parsePerpContexts(perpResp)
	if err != nil {
		return err
	}
	spotCtx, err := parseSpotContexts(spotResp)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.perpCtx = perpCtx
	m.spotCtx = spotCtx
	m.lastCtxRefresh = time.Now().UTC()
	for asset, ctx := range perpCtx {
		m.funding[asset] = ctx.FundingRate
		if ctx.OraclePrice > 0 {
			m.oraclePrices[asset] = ctx.OraclePrice
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *MarketData) shouldRefresh() bool {
	m.mu.RLock()
	last := m.lastCtxRefresh
	window := m.ctxRefreshWindow
	m.mu.RUnlock()
	if last.IsZero() {
		return true
	}
	return time.Since(last) >= window
}

func (m *MarketData) Mid(ctx context.Context, asset string) (float64, error) {
	m.mu.RLock()
	price, ok := m.midPrices[asset]
	m.mu.RUnlock()
	if ok {
		return price, nil
	}
	resp, err := m.rest.Info(ctx, rest.InfoRequest{Type: "allMids"})
	if err != nil {
		return 0, err
	}
	m.updateMids(resp)
	m.mu.RLock()
	price, ok = m.midPrices[asset]
	m.mu.RUnlock()
	if !ok {
		return 0, errors.New("mid price not found")
	}
	return price, nil
}

func (m *MarketData) FundingRate(asset string) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.funding[asset]
	return val, ok
}

func (m *MarketData) OraclePrice(asset string) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.oraclePrices[asset]
	return val, ok
}

func (m *MarketData) SpotContext(asset string) (SpotContext, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ctx, ok := m.spotCtx[asset]
	return ctx, ok
}

func (m *MarketData) PerpContext(asset string) (PerpContext, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ctx, ok := m.perpCtx[asset]
	return ctx, ok
}

func (m *MarketData) PerpAssetID(asset string) (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ctx, ok := m.perpCtx[asset]
	if !ok {
		return 0, false
	}
	return ctx.Index, true
}

func (m *MarketData) SpotAssetID(asset string) (int, bool) {
	m.mu.RLock()
	ctx, ok := m.spotCtx[asset]
	m.mu.RUnlock()
	if !ok && !strings.Contains(asset, "/") {
		m.mu.RLock()
		ctx, ok = m.spotCtx[asset+"/USDC"]
		m.mu.RUnlock()
	}
	if !ok {
		return 0, false
	}
	return 10000 + ctx.Index, true
}

func (m *MarketData) Volatility(asset string) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.volatility[asset]
	return val, ok
}

func (m *MarketData) handleMessage(msg json.RawMessage) {
	var payload map[string]any
	if err := json.Unmarshal(msg, &payload); err != nil {
		m.log.Debug("ws decode error", zap.Error(err))
		return
	}
	m.updateMids(payload)
	m.updateCandle(payload)
}

func (m *MarketData) updateMids(payload map[string]any) {
	var mids map[string]any
	if data, ok := payload["data"].(map[string]any); ok {
		if raw, ok := data["mids"].(map[string]any); ok {
			mids = raw
		}
	}
	if mids == nil {
		if raw, ok := payload["mids"].(map[string]any); ok {
			mids = raw
		}
	}
	if mids == nil {
		// /info allMids returns a flat map of symbol -> mid.
		if _, hasData := payload["data"]; !hasData {
			mids = payload
		}
	}
	if mids == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for asset, v := range mids {
		if f, ok := floatFromAny(v); ok {
			m.midPrices[asset] = f
		}
	}
}

func (m *MarketData) updateCandle(payload map[string]any) {
	asset, close, ok := parseCandle(payload)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	closes := append(m.candleCloses[asset], close)
	if len(closes) > m.candleWindow {
		closes = closes[len(closes)-m.candleWindow:]
	}
	m.candleCloses[asset] = closes
	m.volatility[asset] = computeVolatility(closes)
}

func computeVolatility(closes []float64) float64 {
	if len(closes) < 2 {
		return 0
	}
	var sum float64
	var sumSq float64
	var count float64
	for i := 1; i < len(closes); i++ {
		prev := closes[i-1]
		curr := closes[i]
		if prev == 0 {
			continue
		}
		r := (curr - prev) / prev
		sum += r
		sumSq += r * r
		count++
	}
	if count == 0 {
		return 0
	}
	mean := sum / count
	variance := sumSq/count - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
