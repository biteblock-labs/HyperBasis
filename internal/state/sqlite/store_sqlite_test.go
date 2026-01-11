package sqlite

import (
	"context"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	store, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Set(ctx, "key", "value"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	val, ok, err := store.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !ok || val != "value" {
		t.Fatalf("unexpected value: %v (ok=%v)", val, ok)
	}
	if err := store.Delete(ctx, "key"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	_, ok, err = store.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if ok {
		t.Fatalf("expected key to be deleted")
	}
}
