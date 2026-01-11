package state

import (
	"context"
	"sync"
	"testing"
)

type memoryStore struct {
	mu    sync.Mutex
	items map[string]string
}

func (m *memoryStore) Get(ctx context.Context, key string) (string, bool, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.items[key]
	return val, ok, nil
}

func (m *memoryStore) Set(ctx context.Context, key, value string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = make(map[string]string)
	}
	m.items[key] = value
	return nil
}

func (m *memoryStore) Delete(ctx context.Context, key string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, key)
	return nil
}

func (m *memoryStore) Close() error {
	return nil
}

func TestStrategySnapshotRoundTrip(t *testing.T) {
	store := &memoryStore{}
	ctx := context.Background()
	snapshot := StrategySnapshot{
		Action:       "ENTER",
		SpotAsset:    "UBTC",
		PerpAsset:    "BTC",
		SpotMidPrice: 100,
		PerpMidPrice: 101,
		SpotBalance:  1.25,
		PerpPosition: -1.25,
		OpenOrders:   2,
		UpdatedAtMS:  12345,
	}
	if err := SaveStrategySnapshot(ctx, store, snapshot); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	got, ok, err := LoadStrategySnapshot(ctx, store)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if !ok {
		t.Fatalf("expected snapshot to be present")
	}
	if got != snapshot {
		t.Fatalf("unexpected snapshot: %#v", got)
	}
}

func TestStrategySnapshotMissing(t *testing.T) {
	store := &memoryStore{}
	got, ok, err := LoadStrategySnapshot(context.Background(), store)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if ok {
		t.Fatalf("expected no snapshot, got %#v", got)
	}
}

func TestStrategySnapshotInvalid(t *testing.T) {
	store := &memoryStore{items: map[string]string{StrategySnapshotKey: "{"}}
	_, _, err := LoadStrategySnapshot(context.Background(), store)
	if err == nil {
		t.Fatalf("expected error for invalid snapshot JSON")
	}
}
