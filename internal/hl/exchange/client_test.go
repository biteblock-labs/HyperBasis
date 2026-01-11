package exchange

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"hl-carry-bot/internal/state/sqlite"

	"go.uber.org/zap"
)

func TestNextNonceAtLeastNow(t *testing.T) {
	c := &Client{}
	start := uint64(time.Now().UnixMilli())
	nonce := c.nextNonce()
	if nonce < start {
		t.Fatalf("expected nonce >= %d, got %d", start, nonce)
	}
}

func TestNextNonceMonotonicWhenTimeDoesNotAdvance(t *testing.T) {
	c := &Client{}
	base := uint64(time.Now().UnixMilli()) + 86_400_000
	c.lastNonce.Store(base)
	if got := c.nextNonce(); got != base+1 {
		t.Fatalf("expected %d, got %d", base+1, got)
	}
	if got := c.nextNonce(); got != base+2 {
		t.Fatalf("expected %d, got %d", base+2, got)
	}
}

func TestNextNonceConcurrentUnique(t *testing.T) {
	c := &Client{}
	base := uint64(time.Now().UnixMilli()) + 86_400_000
	c.lastNonce.Store(base)

	const n = 128
	results := make([]uint64, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = c.nextNonce()
		}(i)
	}
	wg.Wait()

	seen := make(map[uint64]struct{}, n)
	min := uint64(0)
	max := uint64(0)
	for i, nonce := range results {
		if _, ok := seen[nonce]; ok {
			t.Fatalf("duplicate nonce %d at index %d", nonce, i)
		}
		seen[nonce] = struct{}{}
		if min == 0 || nonce < min {
			min = nonce
		}
		if nonce > max {
			max = nonce
		}
	}
	if len(seen) != n {
		t.Fatalf("expected %d unique nonces, got %d", n, len(seen))
	}
	if min != base+1 || max != base+n {
		t.Fatalf("expected nonces in range [%d, %d], got [%d, %d]", base+1, base+n, min, max)
	}
}

func TestInitNonceStoreSeedsAndPersists(t *testing.T) {
	signer, err := NewSigner("4f3edf983ac636a65a842ce7c78d9aa706d3b113bce036f81af8f9b72d3d80b2", true)
	if err != nil {
		t.Fatalf("signer error: %v", err)
	}
	store, err := sqlite.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store init: %v", err)
	}
	ctx := context.Background()
	client, err := NewClient("https://api.hyperliquid.xyz", 2*time.Second, signer, "")
	if err != nil {
		t.Fatalf("client init: %v", err)
	}
	client.SetLogger(zap.NewNop())
	seed := uint64(time.Now().UnixMilli()) + 10_000
	key := nonceStoreKey(client.baseURL, client.signer, client.vaultAddress)
	if err := store.Set(ctx, key, strconv.FormatUint(seed, 10)); err != nil {
		t.Fatalf("store seed: %v", err)
	}
	if err := client.InitNonceStore(ctx, store); err != nil {
		t.Fatalf("init nonce store: %v", err)
	}
	if state, ok := client.NonceState(); !ok {
		t.Fatalf("expected nonce state")
	} else if state.Key == "" || state.Last != seed || state.Persisted != seed {
		t.Fatalf("unexpected nonce state: %+v", state)
	}
	nonce := client.nextNonce()
	if nonce != seed+1 {
		t.Fatalf("expected nonce %d, got %d", seed+1, nonce)
	}
	if state, ok := client.NonceState(); !ok {
		t.Fatalf("expected nonce state after update")
	} else if state.Last != nonce || state.Persisted != nonce {
		t.Fatalf("expected nonce state %d, got %+v", nonce, state)
	}
	raw, ok, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if !ok {
		t.Fatalf("expected stored nonce")
	}
	persisted, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		t.Fatalf("parse stored nonce: %v", err)
	}
	if persisted != nonce {
		t.Fatalf("expected stored nonce %d, got %d", nonce, persisted)
	}
}
