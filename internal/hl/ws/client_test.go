package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"
)

func TestClientSendsPing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	msgCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept ws: %v", err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			select {
			case msgCh <- msg:
			default:
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(wsURL, 10*time.Millisecond, 20*time.Millisecond, zap.NewNop())
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() {
		_ = client.Run(runCtx, nil)
	}()

	select {
	case msg := <-msgCh:
		if msg["method"] != "ping" {
			t.Fatalf("expected ping message, got %v", msg)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for ping")
	}
}
