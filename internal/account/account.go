package account

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"hl-carry-bot/internal/hl/rest"
)

type Account struct {
	rest *rest.Client
}

type State struct {
	SpotBalances  map[string]float64
	PerpPosition  map[string]float64
	OpenOrders    []map[string]any
	LastRawUpdate map[string]any
}

func New(restClient *rest.Client) *Account {
	return &Account{rest: restClient}
}

func (a *Account) Reconcile(ctx context.Context) (*State, error) {
	spot, err := a.rest.Info(ctx, rest.InfoRequest{Type: "spotClearinghouseState"})
	if err != nil {
		return nil, err
	}
	perp, err := a.rest.Info(ctx, rest.InfoRequest{Type: "clearinghouseState"})
	if err != nil {
		return nil, err
	}
	orders, err := a.rest.InfoAny(ctx, rest.InfoRequest{Type: "openOrders"})
	if err != nil {
		return nil, err
	}
	return &State{
		SpotBalances:  parseBalances(spot),
		PerpPosition:  parsePositions(perp),
		OpenOrders:    parseOpenOrders(orders),
		LastRawUpdate: map[string]any{"spot": spot, "perp": perp, "orders": orders},
	}, nil
}

func parseBalances(payload map[string]any) map[string]float64 {
	balances := make(map[string]float64)
	if payload == nil {
		return balances
	}
	raw, ok := payload["balances"].([]any)
	if !ok || len(raw) == 0 {
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
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func stringFromAny(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		return strconv.FormatFloat(val, 'f', 0, 64)
	case int:
		return strconv.Itoa(val)
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
