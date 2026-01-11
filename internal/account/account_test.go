package account

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"go.uber.org/zap"
)

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

func TestParseFills(t *testing.T) {
	payload := []any{
		map[string]any{
			"oid":  101,
			"coin": "BTC",
			"side": "B",
			"sz":   "0.5",
			"px":   "30000",
			"time": 1700000000000,
			"hash": "0xdeadbeef",
		},
	}
	fills := parseFills(payload)
	if len(fills) != 1 {
		t.Fatalf("expected 1 fill, got %d", len(fills))
	}
	fill := fills[0]
	if fill.OrderID != "101" {
		t.Fatalf("expected order id 101, got %s", fill.OrderID)
	}
	if fill.Asset != "BTC" {
		t.Fatalf("expected asset BTC, got %s", fill.Asset)
	}
	if fill.Size != 0.5 {
		t.Fatalf("expected size 0.5, got %f", fill.Size)
	}
	if fill.Price != 30000 {
		t.Fatalf("expected price 30000, got %f", fill.Price)
	}
	if fill.TimeMS != 1700000000000 {
		t.Fatalf("expected time 1700000000000, got %d", fill.TimeMS)
	}
	if fill.Hash != "0xdeadbeef" {
		t.Fatalf("expected hash 0xdeadbeef, got %s", fill.Hash)
	}
}

func TestOpenOrdersSnapshotAndDelta(t *testing.T) {
	acct := &Account{log: zap.NewNop()}
	snapshot := map[string]any{
		"channel": "openOrders",
		"data": map[string]any{
			"isSnapshot": true,
			"orders": []any{
				map[string]any{"oid": "1", "coin": "BTC", "status": "open"},
				map[string]any{"oid": "2", "coin": "ETH", "status": "open"},
			},
		},
	}
	raw, _ := json.Marshal(snapshot)
	acct.handleMessage(raw)
	state := acct.Snapshot()
	if len(state.OpenOrders) != 2 {
		t.Fatalf("expected 2 open orders, got %d", len(state.OpenOrders))
	}

	delta := map[string]any{
		"channel": "openOrders",
		"data": map[string]any{
			"isSnapshot": false,
			"orders": []any{
				map[string]any{"oid": "2", "coin": "ETH", "status": "filled"},
				map[string]any{"oid": "3", "coin": "SOL", "status": "open"},
			},
		},
	}
	raw, _ = json.Marshal(delta)
	acct.handleMessage(raw)
	state = acct.Snapshot()
	ids := OpenOrderIDs(state.OpenOrders)
	if len(ids) != 2 {
		t.Fatalf("expected 2 open orders after delta, got %d", len(ids))
	}
	if contains(ids, "2") {
		t.Fatalf("expected order 2 to be removed, got %v", ids)
	}
}

func TestClearinghouseSnapshotAndDelta(t *testing.T) {
	acct := &Account{log: zap.NewNop()}
	snapshot := map[string]any{
		"channel": "clearinghouseState",
		"data": map[string]any{
			"isSnapshot": true,
			"assetPositions": []any{
				map[string]any{"position": map[string]any{"coin": "BTC", "szi": "-0.1"}},
				map[string]any{"position": map[string]any{"coin": "ETH", "szi": "0.2"}},
			},
		},
	}
	raw, _ := json.Marshal(snapshot)
	acct.handleMessage(raw)
	state := acct.Snapshot()
	if state.PerpPosition["BTC"] != -0.1 {
		t.Fatalf("expected BTC -0.1, got %f", state.PerpPosition["BTC"])
	}
	if state.PerpPosition["ETH"] != 0.2 {
		t.Fatalf("expected ETH 0.2, got %f", state.PerpPosition["ETH"])
	}

	delta := map[string]any{
		"channel": "clearinghouseState",
		"data": map[string]any{
			"isSnapshot": false,
			"assetPositions": []any{
				map[string]any{"position": map[string]any{"coin": "BTC", "szi": "0"}},
				map[string]any{"position": map[string]any{"coin": "ETH", "szi": "0.3"}},
			},
		},
	}
	raw, _ = json.Marshal(delta)
	acct.handleMessage(raw)
	state = acct.Snapshot()
	if _, ok := state.PerpPosition["BTC"]; ok {
		t.Fatalf("expected BTC to be removed")
	}
	if state.PerpPosition["ETH"] != 0.3 {
		t.Fatalf("expected ETH 0.3, got %f", state.PerpPosition["ETH"])
	}
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestUserFillsAggregationAndDedupe(t *testing.T) {
	acct := &Account{log: zap.NewNop()}
	update := map[string]any{
		"fills": []any{
			map[string]any{
				"oid":  "1",
				"coin": "BTC",
				"side": "B",
				"sz":   "0.1",
				"px":   "30000",
				"time": 1700000000000,
				"hash": "h1",
			},
			map[string]any{
				"oid":  "1",
				"coin": "BTC",
				"side": "B",
				"sz":   -0.2,
				"px":   30000.0,
				"time": 1700000000001,
				"hash": "h2",
			},
			map[string]any{
				"oid":  "2",
				"coin": "ETH",
				"side": "S",
				"sz":   "0.4",
				"px":   "2000",
				"time": 1700000000002,
			},
		},
	}
	acct.applyUserFillsUpdate(update)
	if got := acct.FillSize("1"); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("expected aggregated fill 0.3 for order 1, got %f", got)
	}
	if got := acct.FillSize("2"); math.Abs(got-0.4) > 1e-9 {
		t.Fatalf("expected aggregated fill 0.4 for order 2, got %f", got)
	}

	acct.applyUserFillsUpdate(update)
	if got := acct.FillSize("1"); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("expected deduped fill 0.3 for order 1, got %f", got)
	}
	if got := acct.FillSize("2"); math.Abs(got-0.4) > 1e-9 {
		t.Fatalf("expected deduped fill 0.4 for order 2, got %f", got)
	}
}

func TestUserFillsEvictsOldOrderIDs(t *testing.T) {
	acct := &Account{log: zap.NewNop()}
	fills := make([]any, 0, maxFillOrderIDs+1)
	for i := 0; i < maxFillOrderIDs+1; i++ {
		fills = append(fills, map[string]any{
			"oid":  strconv.Itoa(i),
			"coin": "BTC",
			"side": "B",
			"sz":   "1",
			"px":   "30000",
			"time": 1700000000000 + int64(i),
		})
	}
	acct.applyUserFillsUpdate(map[string]any{"fills": fills})
	if got := len(acct.fillsByOrderID); got != maxFillOrderIDs {
		t.Fatalf("expected %d tracked orders, got %d", maxFillOrderIDs, got)
	}
	if got := acct.FillSize("0"); got != 0 {
		t.Fatalf("expected order 0 to be evicted, got fill %f", got)
	}
	lastID := strconv.Itoa(maxFillOrderIDs)
	if got := acct.FillSize(lastID); got == 0 {
		t.Fatalf("expected last order %s to remain, got fill %f", lastID, got)
	}
}
