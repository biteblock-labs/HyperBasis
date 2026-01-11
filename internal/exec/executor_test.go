package exec

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"
)

type memoryStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: make(map[string]string)}
}

func (m *memoryStore) Get(ctx context.Context, key string) (string, bool, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.data[key]
	return val, ok, nil
}

func (m *memoryStore) Set(ctx context.Context, key, value string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *memoryStore) Delete(ctx context.Context, key string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memoryStore) Close() error { return nil }

type mockRest struct {
	mu      sync.Mutex
	calls   int
	orderID string
}

func (m *mockRest) PlaceOrder(ctx context.Context, order Order) (string, error) {
	_ = ctx
	_ = order
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.orderID, nil
}

func (m *mockRest) CancelOrder(ctx context.Context, cancel Cancel) error {
	_ = ctx
	_ = cancel
	return nil
}

func TestExecutorIdempotentPlacement(t *testing.T) {
	store := newMemoryStore()
	rest := &mockRest{orderID: "oid-1"}
	logger := zap.NewNop()
	executor := New(rest, store, logger)

	ctx := context.Background()
	order := Order{Asset: 1, IsBuy: true, Size: 1, ClientOrderID: "abc"}

	id1, err := executor.PlaceOrder(ctx, order)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id2, err := executor.PlaceOrder(ctx, order)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected same order id, got %s and %s", id1, id2)
	}
	if rest.calls != 1 {
		t.Fatalf("expected 1 rest call, got %d", rest.calls)
	}

	rest2 := &mockRest{orderID: "oid-2"}
	executor2 := New(rest2, store, logger)
	id3, err := executor2.PlaceOrder(ctx, order)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id3 != id1 {
		t.Fatalf("expected stored order id %s, got %s", id1, id3)
	}
	if rest2.calls != 0 {
		t.Fatalf("expected no rest calls on restart, got %d", rest2.calls)
	}
}
