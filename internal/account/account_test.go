package account

import "testing"

func TestParseBalances(t *testing.T) {
	payload := map[string]any{
		"balances": []any{
			map[string]any{"coin": "USDC", "total": "100.5"},
			map[string]any{"token": "BTC", "available": 0.01},
		},
	}

	balances := parseBalances(payload)
	if balances["USDC"] != 100.5 {
		t.Fatalf("expected USDC 100.5, got %f", balances["USDC"])
	}
	if balances["BTC"] != 0.01 {
		t.Fatalf("expected BTC 0.01, got %f", balances["BTC"])
	}
}

func TestParsePositions(t *testing.T) {
	payload := map[string]any{
		"assetPositions": []any{
			map[string]any{
				"position": map[string]any{"coin": "BTC", "szi": "-0.1"},
			},
			map[string]any{
				"position": map[string]any{"coin": "ETH", "size": 0.5},
			},
		},
	}

	positions := parsePositions(payload)
	if positions["BTC"] != -0.1 {
		t.Fatalf("expected BTC -0.1, got %f", positions["BTC"])
	}
	if positions["ETH"] != 0.5 {
		t.Fatalf("expected ETH 0.5, got %f", positions["ETH"])
	}
}

func TestParseOpenOrders(t *testing.T) {
	payload := []any{
		map[string]any{"oid": "1", "coin": "BTC"},
		map[string]any{"oid": 2, "coin": "ETH"},
	}
	orders := parseOpenOrders(payload)
	if len(orders) != 2 {
		t.Fatalf("expected 2 orders, got %d", len(orders))
	}
	ids := OpenOrderIDs(orders)
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "2" {
		t.Fatalf("unexpected order ids: %v", ids)
	}

	payloadMap := map[string]any{"openOrders": payload}
	orders = parseOpenOrders(payloadMap)
	if len(orders) != 2 {
		t.Fatalf("expected 2 orders from map, got %d", len(orders))
	}
}
