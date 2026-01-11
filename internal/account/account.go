package account

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"hl-carry-bot/internal/hl/rest"
	"hl-carry-bot/internal/hl/ws"

	"go.uber.org/zap"
)

type Account struct {
	rest *rest.Client
	ws   *ws.Client
	log  *zap.Logger
	user string

	mu                     sync.RWMutex
	state                  State
	openOrders             map[string]map[string]any
	fillsEnabled           bool
	fillsByOrderID         map[string]float64
	fillOrderList          *list.List
	fillOrderElem          map[string]*list.Element
	seenFillKeys           map[string]struct{}
	seenFillOrder          []string
	hasOpenOrdersSnapshot  bool
	hasPerpStateSnapshot   bool
	hasSpotStateSnapshot   bool
	lastClearinghouseState map[string]any
	spotPostID             atomic.Uint64
}

const (
	maxSeenFillKeys = 2000
	maxFillOrderIDs = 2000
	balanceEpsilon  = 1e-9
)

type State struct {
	SpotBalances  map[string]float64
	PerpPosition  map[string]float64
	OpenOrders    []map[string]any
	LastRawUpdate map[string]any
}

func New(restClient *rest.Client, wsClient *ws.Client, log *zap.Logger, user string) *Account {
	return &Account{rest: restClient, ws: wsClient, log: log, user: strings.TrimSpace(user)}
}

func (a *Account) Reconcile(ctx context.Context) (*State, error) {
	if a.rest == nil {
		return nil, errors.New("rest client is required")
	}
	spot, err := a.rest.Info(ctx, rest.InfoRequest{Type: "spotClearinghouseState", User: a.user})
	if err != nil {
		return nil, err
	}
	perp, err := a.rest.Info(ctx, rest.InfoRequest{Type: "clearinghouseState", User: a.user})
	if err != nil {
		return nil, err
	}
	orders, err := a.rest.InfoAny(ctx, rest.InfoRequest{Type: "openOrders", User: a.user})
	if err != nil {
		return nil, err
	}
	state := State{
		SpotBalances:  parseBalances(spot),
		PerpPosition:  parsePositions(perp),
		OpenOrders:    parseOpenOrders(orders),
		LastRawUpdate: map[string]any{"spot": spot, "perp": perp, "orders": orders},
	}
	a.mu.Lock()
	a.state = state
	a.openOrders = openOrdersMap(state.OpenOrders)
	a.hasOpenOrdersSnapshot = true
	a.hasPerpStateSnapshot = true
	a.hasSpotStateSnapshot = true
	a.lastClearinghouseState = perp
	a.mu.Unlock()
	return &state, nil
}

func (a *Account) Start(ctx context.Context) error {
	if a.ws == nil {
		return nil
	}
	if a.user == "" {
		return errors.New("account user is required for ws subscriptions")
	}
	if err := a.ws.Connect(ctx); err != nil {
		return err
	}
	openOrdersSub := map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type": "openOrders",
			"user": a.user,
		},
	}
	if err := a.ws.Subscribe(ctx, openOrdersSub); err != nil {
		return err
	}
	perpSub := map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type": "clearinghouseState",
			"user": a.user,
		},
	}
	if err := a.ws.Subscribe(ctx, perpSub); err != nil {
		return err
	}
	fillsSub := map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type": "userFills",
			"user": a.user,
		},
	}
	if err := a.ws.Subscribe(ctx, fillsSub); err != nil {
		return err
	}
	ledgerSub := map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type": "userNonFundingLedgerUpdates",
			"user": a.user,
		},
	}
	if err := a.ws.Subscribe(ctx, ledgerSub); err != nil {
		return err
	}
	a.mu.Lock()
	a.fillsEnabled = true
	a.mu.Unlock()
	go func() {
		_ = a.ws.Run(ctx, a.handleMessage)
	}()
	return nil
}

func (a *Account) Snapshot() State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return copyState(a.state)
}

func (a *Account) FillsEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fillsEnabled
}

func (a *Account) FillSize(orderID string) float64 {
	if orderID == "" {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fillsByOrderID[orderID]
}

func (a *Account) handleMessage(msg json.RawMessage) {
	var payload map[string]any
	if err := json.Unmarshal(msg, &payload); err != nil {
		if a.log != nil {
			a.log.Debug("account ws decode failed", zap.Error(err))
		}
		return
	}
	channel := stringFromAny(payload["channel"])
	switch channel {
	case "openOrders":
		a.applyOpenOrdersUpdate(payload["data"])
	case "clearinghouseState":
		a.applyClearinghouseUpdate(payload["data"])
	case "userFills":
		a.applyUserFillsUpdate(payload["data"])
	case "userNonFundingLedgerUpdates":
		a.applyLedgerUpdates(payload["data"])
	}
}

func (a *Account) applyOpenOrdersUpdate(data any) {
	orders := parseOpenOrders(data)
	isSnapshot, hasSnapshot := snapshotFlag(data)
	if len(orders) == 0 && !hasSnapshot {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if isSnapshot || !a.hasOpenOrdersSnapshot {
		a.openOrders = openOrdersMap(orders)
		a.state.OpenOrders = openOrdersSlice(a.openOrders)
		a.hasOpenOrdersSnapshot = true
	} else {
		if a.openOrders == nil {
			a.openOrders = openOrdersMap(a.state.OpenOrders)
		}
		for _, order := range orders {
			id := orderIDFromOrder(order)
			if id == "" {
				continue
			}
			if orderIsTerminal(order) {
				delete(a.openOrders, id)
				continue
			}
			a.openOrders[id] = order
		}
		a.state.OpenOrders = openOrdersSlice(a.openOrders)
	}
	if a.state.LastRawUpdate == nil {
		a.state.LastRawUpdate = make(map[string]any)
	}
	a.state.LastRawUpdate["ws_open_orders"] = data
}

func (a *Account) applyClearinghouseUpdate(data any) {
	payload, ok := data.(map[string]any)
	if !ok {
		return
	}
	isSnapshot, hasSnapshot := snapshotFlag(payload)
	positions := parsePositions(payload)
	if len(positions) == 0 {
		if nested, ok := payload["data"].(map[string]any); ok {
			positions = parsePositions(nested)
		}
	}
	if len(positions) == 0 && !hasSnapshot {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if isSnapshot || !a.hasPerpStateSnapshot {
		a.state.PerpPosition = positions
		a.hasPerpStateSnapshot = true
	} else {
		if a.state.PerpPosition == nil {
			a.state.PerpPosition = make(map[string]float64)
		}
		for asset, size := range positions {
			if size == 0 {
				delete(a.state.PerpPosition, asset)
				continue
			}
			a.state.PerpPosition[asset] = size
		}
	}
	a.lastClearinghouseState = payload
	if a.state.LastRawUpdate == nil {
		a.state.LastRawUpdate = make(map[string]any)
	}
	a.state.LastRawUpdate["ws_clearinghouse"] = data
}

func (a *Account) applyUserFillsUpdate(data any) {
	fills := parseFills(data)
	if len(fills) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fillsByOrderID == nil {
		a.fillsByOrderID = make(map[string]float64)
	}
	if a.fillOrderList == nil {
		a.fillOrderList = list.New()
	}
	if a.fillOrderElem == nil {
		a.fillOrderElem = make(map[string]*list.Element)
	}
	if a.seenFillKeys == nil {
		a.seenFillKeys = make(map[string]struct{})
	}
	for _, fill := range fills {
		if fill.OrderID == "" {
			continue
		}
		if fill.Size == 0 {
			continue
		}
		key := fill.Hash
		if key == "" {
			key = fmt.Sprintf("%s:%d:%s:%s", fill.OrderID, fill.TimeMS, floatKey(fill.Size), floatKey(fill.Price))
		}
		if _, ok := a.seenFillKeys[key]; ok {
			continue
		}
		a.seenFillKeys[key] = struct{}{}
		a.seenFillOrder = append(a.seenFillOrder, key)
		if elem, ok := a.fillOrderElem[fill.OrderID]; ok {
			a.fillOrderList.MoveToBack(elem)
		} else {
			elem := a.fillOrderList.PushBack(fill.OrderID)
			a.fillOrderElem[fill.OrderID] = elem
		}
		a.fillsByOrderID[fill.OrderID] += math.Abs(fill.Size)
	}
	if len(a.seenFillOrder) > maxSeenFillKeys {
		evict := a.seenFillOrder[0 : len(a.seenFillOrder)-maxSeenFillKeys]
		for _, key := range evict {
			delete(a.seenFillKeys, key)
		}
		a.seenFillOrder = a.seenFillOrder[len(a.seenFillOrder)-maxSeenFillKeys:]
	}
	for len(a.fillsByOrderID) > maxFillOrderIDs {
		if a.fillOrderList == nil {
			break
		}
		front := a.fillOrderList.Front()
		if front == nil {
			break
		}
		orderID, ok := front.Value.(string)
		a.fillOrderList.Remove(front)
		if ok {
			delete(a.fillOrderElem, orderID)
			delete(a.fillsByOrderID, orderID)
		}
	}
}

func parseBalances(payload map[string]any) map[string]float64 {
	if payload == nil {
		return make(map[string]float64)
	}
	raw, ok := payload["balances"].([]any)
	if !ok {
		return make(map[string]float64)
	}
	return parseBalanceEntries(raw)
}

func parseBalanceEntries(raw []any) map[string]float64 {
	balances := make(map[string]float64)
	if len(raw) == 0 {
		return balances
	}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		asset := stringFromAny(entry["coin"])
		if asset == "" {
			asset = stringFromAny(entry["token"])
		}
		if asset == "" {
			asset = stringFromAny(entry["symbol"])
		}
		if asset == "" {
			continue
		}
		if val, ok := floatFromAny(entry["total"]); ok {
			balances[asset] = val
			continue
		}
		if val, ok := floatFromAny(entry["balance"]); ok {
			balances[asset] = val
			continue
		}
		if val, ok := floatFromAny(entry["available"]); ok {
			balances[asset] = val
			continue
		}
	}
	return balances
}

func parseSpotBalances(data any) map[string]float64 {
	if data == nil {
		return nil
	}
	switch payload := data.(type) {
	case map[string]any:
		if _, ok := payload["balances"]; ok {
			return parseBalances(payload)
		}
		if nested, ok := payload["data"]; ok {
			return parseSpotBalances(nested)
		}
	case []any:
		return parseBalanceEntries(payload)
	}
	return nil
}

func parseLedgerUpdates(data any) []map[string]any {
	if data == nil {
		return nil
	}
	switch payload := data.(type) {
	case []map[string]any:
		return payload
	case []any:
		return normalizeLedgerUpdates(payload)
	case map[string]any:
		if list, ok := payload["updates"].([]any); ok {
			return normalizeLedgerUpdates(list)
		}
		if list, ok := payload["ledgerUpdates"].([]any); ok {
			return normalizeLedgerUpdates(list)
		}
		if list, ok := payload["data"].([]any); ok {
			return normalizeLedgerUpdates(list)
		}
		if nested, ok := payload["data"].(map[string]any); ok {
			if updates := parseLedgerUpdates(nested); len(updates) > 0 {
				return updates
			}
		}
		if _, ok := payload["type"]; ok {
			return []map[string]any{payload}
		}
	}
	return nil
}

func normalizeLedgerUpdates(raw []any) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	updates := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if entry, ok := item.(map[string]any); ok {
			updates = append(updates, entry)
		}
	}
	return updates
}

func ledgerSnapshot(data any) bool {
	if isSnapshot, has := snapshotFlag(data); has {
		return isSnapshot
	}
	if payload, ok := data.(map[string]any); ok {
		if nested, ok := payload["data"]; ok {
			if isSnapshot, has := snapshotFlag(nested); has {
				return isSnapshot
			}
		}
	}
	return false
}

func signedLedgerAmount(amount float64, update map[string]any, user string) float64 {
	if amount == 0 {
		return 0
	}
	if amount < 0 {
		return amount
	}
	me := normalizeAddr(user)
	if me == "" {
		return amount
	}
	dest := normalizeAddr(stringFromAny(update["destination"]))
	if dest != "" && dest == me {
		return amount
	}
	from := normalizeAddr(stringFromAny(update["user"]))
	if from != "" && from == me {
		return -amount
	}
	return amount
}

func ledgerDelta(update map[string]any, user string) (string, float64, bool) {
	switch strings.ToLower(stringFromAny(update["type"])) {
	case "spottransfer":
		asset := stringFromAny(update["token"])
		if asset == "" {
			asset = stringFromAny(update["coin"])
		}
		amount, ok := floatFromAny(update["amount"])
		if !ok {
			return "", 0, false
		}
		delta := signedLedgerAmount(amount, update, user)
		if asset == "" || delta == 0 {
			return "", 0, false
		}
		return asset, delta, true
	case "spotgenesis":
		asset := stringFromAny(update["token"])
		amount, ok := floatFromAny(update["amount"])
		if !ok {
			return "", 0, false
		}
		if asset == "" || amount == 0 {
			return "", 0, false
		}
		return asset, amount, true
	case "accountclasstransfer":
		usdc, ok := floatFromAny(update["usdc"])
		if !ok {
			return "", 0, false
		}
		toPerp, ok := boolFromAny(update["toPerp"])
		if !ok {
			return "", 0, false
		}
		if usdc == 0 {
			return "", 0, false
		}
		if toPerp {
			return "USDC", -usdc, true
		}
		return "USDC", usdc, true
	}
	asset := stringFromAny(update["token"])
	if asset == "" {
		return "", 0, false
	}
	amount, ok := floatFromAny(update["amount"])
	if !ok || amount == 0 {
		return "", 0, false
	}
	return asset, amount, true
}

func (a *Account) applyLedgerUpdates(data any) {
	updates := parseLedgerUpdates(data)
	if len(updates) == 0 {
		return
	}
	if ledgerSnapshot(data) {
		a.mu.Lock()
		if a.state.LastRawUpdate == nil {
			a.state.LastRawUpdate = make(map[string]any)
		}
		a.state.LastRawUpdate["ws_user_non_funding_ledger"] = data
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.hasSpotStateSnapshot {
		return
	}
	if a.state.SpotBalances == nil {
		a.state.SpotBalances = make(map[string]float64)
	}
	for _, update := range updates {
		asset, delta, ok := ledgerDelta(update, a.user)
		if !ok {
			continue
		}
		next := a.state.SpotBalances[asset] + delta
		if math.Abs(next) <= balanceEpsilon {
			delete(a.state.SpotBalances, asset)
			continue
		}
		a.state.SpotBalances[asset] = next
	}
	if a.state.LastRawUpdate == nil {
		a.state.LastRawUpdate = make(map[string]any)
	}
	a.state.LastRawUpdate["ws_user_non_funding_ledger"] = data
	a.hasSpotStateSnapshot = true
}

func (a *Account) RefreshSpotBalancesWS(ctx context.Context) error {
	if a.ws == nil {
		return nil
	}
	if a.user == "" {
		return errors.New("account user is required")
	}
	req := map[string]any{
		"type": "info",
		"payload": map[string]any{
			"type": "spotClearinghouseState",
			"user": a.user,
		},
	}
	postID := a.spotPostID.Add(1)
	raw, err := a.ws.Post(ctx, postID, req)
	if err != nil {
		return err
	}
	balances, err := parseSpotBalancesPost(raw)
	if err != nil {
		return err
	}
	if balances == nil {
		return nil
	}
	a.mu.Lock()
	a.state.SpotBalances = balances
	a.hasSpotStateSnapshot = true
	if a.state.LastRawUpdate == nil {
		a.state.LastRawUpdate = make(map[string]any)
	}
	a.state.LastRawUpdate["ws_post_spot_clearinghouse"] = map[string]any{"id": postID}
	a.mu.Unlock()
	return nil
}

func parseSpotBalancesPost(raw json.RawMessage) (map[string]float64, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	channel := stringFromAny(payload["channel"])
	if channel != "post" {
		return nil, fmt.Errorf("unexpected post channel %q", channel)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil, errors.New("post data missing")
	}
	response, ok := data["response"].(map[string]any)
	if !ok {
		return nil, errors.New("post response missing")
	}
	if stringFromAny(response["type"]) == "error" {
		return nil, fmt.Errorf("post error: %s", stringFromAny(response["payload"]))
	}
	payloadMap, ok := response["payload"].(map[string]any)
	if !ok {
		return nil, errors.New("post payload missing")
	}
	if typ := stringFromAny(payloadMap["type"]); typ != "spotClearinghouseState" {
		return nil, fmt.Errorf("unexpected post payload type %q", typ)
	}
	balances := parseSpotBalances(payloadMap["data"])
	if balances == nil {
		return nil, errors.New("spot balances missing")
	}
	return balances, nil
}

func parsePositions(payload map[string]any) map[string]float64 {
	positions := make(map[string]float64)
	if payload == nil {
		return positions
	}
	raw, ok := payload["assetPositions"].([]any)
	if !ok || len(raw) == 0 {
		return positions
	}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pos := entry
		if nested, ok := entry["position"].(map[string]any); ok {
			pos = nested
		}
		asset := stringFromAny(pos["coin"])
		if asset == "" {
			asset = stringFromAny(pos["symbol"])
		}
		if asset == "" {
			asset = stringFromAny(pos["asset"])
		}
		if asset == "" {
			continue
		}
		size := 0.0
		if val, ok := floatFromAny(pos["szi"]); ok {
			size = val
		} else if val, ok := floatFromAny(pos["size"]); ok {
			size = val
		} else if val, ok := floatFromAny(pos["position"]); ok {
			size = val
		}
		positions[asset] = size
	}
	return positions
}

func parseOpenOrders(payload any) []map[string]any {
	if payload == nil {
		return nil
	}
	if list, ok := payload.([]any); ok {
		return normalizeOrders(list)
	}
	if list, ok := payload.([]map[string]any); ok {
		return list
	}
	if payloadMap, ok := payload.(map[string]any); ok {
		return normalizeOrders(extractOrders(payloadMap))
	}
	return nil
}

func extractOrders(payload map[string]any) []any {
	if list, ok := payload["openOrders"].([]any); ok {
		return list
	}
	if list, ok := payload["orders"].([]any); ok {
		return list
	}
	if list, ok := payload["data"].([]any); ok {
		return list
	}
	return nil
}

func normalizeOrders(raw []any) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	orders := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			orders = append(orders, m)
		}
	}
	return orders
}

func OpenOrderIDs(openOrders []map[string]any) []string {
	ids := make([]string, 0, len(openOrders))
	for _, order := range openOrders {
		id := orderIDFromOrder(order)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

type OrderRef struct {
	OrderID     string
	Cloid       string
	AssetSymbol string
	AssetID     int
}

func OpenOrderRefs(openOrders []map[string]any) []OrderRef {
	refs := make([]OrderRef, 0, len(openOrders))
	for _, order := range openOrders {
		orderID := orderIDFromOrder(order)
		cloid := stringFromAny(order["cloid"])
		if cloid == "" {
			cloid = stringFromAny(order["clientOrderId"])
		}
		assetSymbol := stringFromAny(order["coin"])
		if assetSymbol == "" {
			assetSymbol = stringFromAny(order["symbol"])
		}
		if assetSymbol == "" {
			assetSymbol = stringFromAny(order["asset"])
		}
		assetID := intFromAny(order["asset"])
		if assetID == 0 {
			assetID = intFromAny(order["a"])
		}
		if orderID == "" && cloid == "" {
			continue
		}
		refs = append(refs, OrderRef{
			OrderID:     orderID,
			Cloid:       cloid,
			AssetSymbol: assetSymbol,
			AssetID:     assetID,
		})
	}
	return refs
}

func stringFromAny(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		return strconv.FormatFloat(val, 'f', 0, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case json.Number:
		return val.String()
	default:
		return ""
	}
}

func floatFromAny(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func boolFromAny(v any) (bool, bool) {
	switch val := v.(type) {
	case bool:
		return val, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(val))
		return parsed, err == nil
	case float64:
		return val != 0, true
	case int:
		return val != 0, true
	case json.Number:
		i, err := val.Int64()
		return i != 0, err == nil
	default:
		return false, false
	}
}

func intFromAny(v any) int {
	if f, ok := floatFromAny(v); ok {
		return int(f)
	}
	return 0
}

func int64FromAny(v any) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int:
		return int64(val)
	case float64:
		return int64(val)
	case json.Number:
		i, err := val.Int64()
		if err == nil {
			return i
		}
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err == nil {
			return i
		}
	}
	return 0
}

func snapshotFlag(data any) (bool, bool) {
	if payload, ok := data.(map[string]any); ok {
		if raw, ok := payload["isSnapshot"]; ok {
			if val, ok := boolFromAny(raw); ok {
				return val, true
			}
		}
	}
	return false, false
}

func floatKey(v float64) string {
	return strconv.FormatFloat(v, 'g', 12, 64)
}

func normalizeAddr(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

func orderIDFromOrder(order map[string]any) string {
	id := stringFromAny(order["oid"])
	if id == "" {
		id = stringFromAny(order["orderId"])
	}
	if id == "" {
		id = stringFromAny(order["orderID"])
	}
	if id == "" {
		id = stringFromAny(order["id"])
	}
	return id
}

func openOrdersMap(openOrders []map[string]any) map[string]map[string]any {
	if len(openOrders) == 0 {
		return nil
	}
	result := make(map[string]map[string]any, len(openOrders))
	for _, order := range openOrders {
		if id := orderIDFromOrder(order); id != "" {
			result[id] = order
		}
	}
	return result
}

func openOrdersSlice(openOrders map[string]map[string]any) []map[string]any {
	if len(openOrders) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(openOrders))
	for _, order := range openOrders {
		result = append(result, order)
	}
	return result
}

func orderIsTerminal(order map[string]any) bool {
	if val, ok := boolFromAny(order["isCancelled"]); ok && val {
		return true
	}
	status := strings.ToLower(stringFromAny(order["status"]))
	if status == "" {
		status = strings.ToLower(stringFromAny(order["state"]))
	}
	if status == "" {
		status = strings.ToLower(stringFromAny(order["orderStatus"]))
	}
	if status != "" {
		switch status {
		case "open", "live", "pending":
			return false
		default:
			return true
		}
	}
	if rem, ok := floatFromAny(order["remainingSz"]); ok && rem == 0 {
		return true
	}
	if rem, ok := floatFromAny(order["remainingSize"]); ok && rem == 0 {
		return true
	}
	return false
}

func copyState(state State) State {
	out := State{
		SpotBalances: copyFloatMap(state.SpotBalances),
		PerpPosition: copyFloatMap(state.PerpPosition),
		OpenOrders:   copyOrderSlice(state.OpenOrders),
	}
	if state.LastRawUpdate != nil {
		out.LastRawUpdate = make(map[string]any, len(state.LastRawUpdate))
		for k, v := range state.LastRawUpdate {
			out.LastRawUpdate[k] = v
		}
	}
	return out
}

func copyFloatMap(src map[string]float64) map[string]float64 {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func copyOrderSlice(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(src))
	for _, order := range src {
		copyOrder := make(map[string]any, len(order))
		for k, v := range order {
			copyOrder[k] = v
		}
		out = append(out, copyOrder)
	}
	return out
}
