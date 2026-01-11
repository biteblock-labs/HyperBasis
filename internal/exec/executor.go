package exec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"hl-carry-bot/internal/state"

	"go.uber.org/zap"
)

type Order struct {
	Asset         int
	IsBuy         bool
	Size          float64
	LimitPrice    float64
	ReduceOnly    bool
	ClientOrderID string
}

type RestClient interface {
	PlaceOrder(ctx context.Context, order Order) (string, error)
	CancelOrder(ctx context.Context, orderID string) error
}

type Executor struct {
	rest  RestClient
	store state.Store
	log   *zap.Logger

	mu    sync.Mutex
	cache map[string]string
}

func New(rest RestClient, store state.Store, log *zap.Logger) *Executor {
	return &Executor{
		rest:  rest,
		store: store,
		log:   log,
		cache: make(map[string]string),
	}
}

func (e *Executor) PlaceOrder(ctx context.Context, order Order) (string, error) {
	if order.ClientOrderID == "" {
		return e.placeWithRetry(ctx, order)
	}
	cacheKey := "cloid:" + order.ClientOrderID
	e.mu.Lock()
	if oid, ok := e.cache[cacheKey]; ok {
		e.mu.Unlock()
		return oid, nil
	}
	e.mu.Unlock()
	if e.store != nil {
		if oid, ok, err := e.store.Get(ctx, cacheKey); err != nil {
			return "", err
		} else if ok {
			e.mu.Lock()
			e.cache[cacheKey] = oid
			e.mu.Unlock()
			return oid, nil
		}
	}
	orderID, err := e.placeWithRetry(ctx, order)
	if err != nil {
		return "", err
	}
	if e.store != nil {
		if err := e.store.Set(ctx, cacheKey, orderID); err != nil {
			e.log.Warn("failed to persist order id", zap.Error(err))
		}
	}
	e.mu.Lock()
	e.cache[cacheKey] = orderID
	e.mu.Unlock()
	return orderID, nil
}

func (e *Executor) CancelOrder(ctx context.Context, orderID string) error {
	return e.retry(ctx, func() error {
		return e.rest.CancelOrder(ctx, orderID)
	})
}

func (e *Executor) placeWithRetry(ctx context.Context, order Order) (string, error) {
	var orderID string
	err := e.retry(ctx, func() error {
		var err error
		orderID, err = e.rest.PlaceOrder(ctx, order)
		return err
	})
	if err != nil {
		return "", err
	}
	if orderID == "" {
		return "", errors.New("empty order id")
	}
	return orderID, nil
}

func (e *Executor) retry(ctx context.Context, fn func() error) error {
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < 5; attempt++ {
		if err := fn(); err != nil {
			if attempt == 4 {
				return fmt.Errorf("retry failed: %w", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
			continue
		}
		return nil
	}
	return nil
}
