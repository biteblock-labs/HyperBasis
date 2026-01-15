package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"hl-carry-bot/internal/config"
)

type memoryStore struct {
	mu   sync.Mutex
	data map[string]string
}

func (m *memoryStore) Get(ctx context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.data[key]
	return val, ok, nil
}

func (m *memoryStore) Set(ctx context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string]string)
	}
	m.data[key] = value
	return nil
}

func (m *memoryStore) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memoryStore) Close() error {
	return nil
}

func TestParseOperatorCommand(t *testing.T) {
	cmd, args, ok := parseOperatorCommand("/status now")
	if !ok {
		t.Fatalf("expected ok")
	}
	if cmd != "status" {
		t.Fatalf("expected status, got %s", cmd)
	}
	if len(args) != 1 || args[0] != "now" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestOperatorPauseResumeAudit(t *testing.T) {
	store := &memoryStore{data: make(map[string]string)}
	app := &App{store: store}
	meta := operatorMeta{UserID: 1, ChatID: 2, Raw: "/pause"}

	resp, err := app.handleOperatorCommand(context.Background(), "pause", nil, meta)
	if err != nil {
		t.Fatalf("pause error: %v", err)
	}
	if resp != "trading paused" {
		t.Fatalf("unexpected pause response: %s", resp)
	}
	if !app.isPaused() {
		t.Fatalf("expected paused")
	}

	meta.Raw = "/resume"
	resp, err = app.handleOperatorCommand(context.Background(), "resume", nil, meta)
	if err != nil {
		t.Fatalf("resume error: %v", err)
	}
	if resp != "trading resumed" {
		t.Fatalf("unexpected resume response: %s", resp)
	}
	if app.isPaused() {
		t.Fatalf("expected resumed")
	}
	found := false
	for key := range store.data {
		if strings.HasPrefix(key, "ops:audit:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected audit entry")
	}
}

func TestRiskOverrideSetReset(t *testing.T) {
	store := &memoryStore{data: make(map[string]string)}
	cfg := &config.Config{
		Risk: config.RiskConfig{
			MaxNotionalUSD: 100,
			MaxOpenOrders:  5,
			MaxMarketAge:   2 * time.Minute,
			MaxAccountAge:  5 * time.Minute,
		},
	}
	app := &App{cfg: cfg, store: store}
	meta := operatorMeta{UserID: 1, ChatID: 2, Raw: "/risk set max_notional_usd=200"}

	resp, err := app.handleRiskCommand(context.Background(), []string{"set", "max_notional_usd=200"}, meta)
	if err != nil {
		t.Fatalf("risk set error: %v", err)
	}
	if resp != "risk override updated" {
		t.Fatalf("unexpected response: %s", resp)
	}
	if !app.riskOverrideActive() {
		t.Fatalf("expected risk override active")
	}
	if got := app.riskConfig().MaxNotionalUSD; got != 200 {
		t.Fatalf("expected risk override 200, got %f", got)
	}

	meta.Raw = "/risk reset"
	resp, err = app.handleRiskCommand(context.Background(), []string{"reset"}, meta)
	if err != nil {
		t.Fatalf("risk reset error: %v", err)
	}
	if resp != "risk override cleared" {
		t.Fatalf("unexpected response: %s", resp)
	}
	if app.riskOverrideActive() {
		t.Fatalf("expected risk override cleared")
	}
}

func TestApplyRiskOverridesRejectsUnknownKey(t *testing.T) {
	_, err := applyRiskOverrides(config.RiskConfig{}, map[string]string{"unknown": "1"})
	if err == nil {
		t.Fatalf("expected error for unknown key")
	}
}
