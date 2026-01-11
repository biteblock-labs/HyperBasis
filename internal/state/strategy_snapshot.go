package state

import (
	"context"
	"encoding/json"
	"strings"
)

const StrategySnapshotKey = "strategy:last_snapshot"

type StrategySnapshot struct {
	Action       string  `json:"action"`
	SpotAsset    string  `json:"spot_asset"`
	PerpAsset    string  `json:"perp_asset"`
	SpotMidPrice float64 `json:"spot_mid_price"`
	PerpMidPrice float64 `json:"perp_mid_price"`
	SpotBalance  float64 `json:"spot_balance"`
	PerpPosition float64 `json:"perp_position"`
	OpenOrders   int     `json:"open_orders"`
	UpdatedAtMS  int64   `json:"updated_at_ms"`
}

func LoadStrategySnapshot(ctx context.Context, store Store) (StrategySnapshot, bool, error) {
	if store == nil {
		return StrategySnapshot{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, ok, err := store.Get(ctx, StrategySnapshotKey)
	if err != nil {
		return StrategySnapshot{}, false, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return StrategySnapshot{}, false, nil
	}
	var snapshot StrategySnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return StrategySnapshot{}, false, err
	}
	return snapshot, true, nil
}

func SaveStrategySnapshot(ctx context.Context, store Store, snapshot StrategySnapshot) error {
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return store.Set(ctx, StrategySnapshotKey, string(payload))
}
