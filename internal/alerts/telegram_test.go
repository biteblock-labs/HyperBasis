package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hl-carry-bot/internal/config"

	"go.uber.org/zap"
)

func TestTelegramSendDisabled(t *testing.T) {
	cfg := config.TelegramConfig{Enabled: false}
	client := newTelegram(cfg, zap.NewNop(), "http://unused", nil)
	if err := client.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("expected nil error when disabled, got %v", err)
	}
}

func TestTelegramSendMissingConfig(t *testing.T) {
	cfg := config.TelegramConfig{Enabled: true}
	client := newTelegram(cfg, zap.NewNop(), "http://unused", nil)
	if err := client.Send(context.Background(), "hello"); err == nil {
		t.Fatalf("expected error for missing token/chat_id")
	}
}

func TestTelegramSendPostsMessage(t *testing.T) {
	var gotPath string
	var gotPayload map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	cfg := config.TelegramConfig{Enabled: true, Token: "token", ChatID: "123"}
	client := newTelegram(cfg, zap.NewNop(), server.URL, server.Client())
	if err := client.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("expected send success, got %v", err)
	}
	if gotPath != "/bottoken/sendMessage" {
		t.Fatalf("expected path /bottoken/sendMessage, got %s", gotPath)
	}
	if gotPayload["chat_id"] != "123" {
		t.Fatalf("expected chat_id 123, got %q", gotPayload["chat_id"])
	}
	if gotPayload["text"] != "hello" {
		t.Fatalf("expected text hello, got %q", gotPayload["text"])
	}
}
