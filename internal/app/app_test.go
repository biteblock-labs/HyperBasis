package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hl-carry-bot/internal/account"
	"hl-carry-bot/internal/hl/rest"
	"hl-carry-bot/internal/hl/ws"

	"go.uber.org/zap"
	"nhooyr.io/websocket"
)

func TestRoundDown(t *testing.T) {
	got := roundDown(1.239, 2)
	if math.Abs(got-1.23) > 1e-9 {
		t.Fatalf("expected 1.23, got %f", got)
	}
}

func TestNormalizeLimitPriceDecimals(t *testing.T) {
	price := normalizeLimitPrice(123.456789, true, 2)
	scaled := price * 1e6
	if math.Abs(scaled-math.Round(scaled)) > 1e-9 {
		t.Fatalf("expected spot price rounded to 6 decimals, got %f", price)
	}
	perpPrice := normalizeLimitPrice(123.456789, false, 1)
	perpScaled := perpPrice * 1e5
	if math.Abs(perpScaled-math.Round(perpScaled)) > 1e-9 {
		t.Fatalf("expected perp price rounded to 5 decimals, got %f", perpPrice)
	}
}

func TestIsFlat(t *testing.T) {
	if !isFlat(0, 0) {
		t.Fatalf("expected flat state")
	}
	if isFlat(1e-3, 0) {
		t.Fatalf("expected non-flat spot balance")
	}
	if isFlat(0, 1e-3) {
		t.Fatalf("expected non-flat perp position")
	}
}

func TestWaitForOrderFillRestFallbackRetryOnError(t *testing.T) {
	var userFillsCalls atomic.Int32
	handlerErrCh := make(chan error, 1)
	recordHandlerErr := func(err error) {
		select {
		case handlerErrCh <- err:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			recordHandlerErr(fmt.Errorf("read body: %w", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			recordHandlerErr(fmt.Errorf("decode body: %w", err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch payload["type"] {
		case "openOrders":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "userFillsByTime":
			callNum := userFillsCalls.Add(1)
			if callNum == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"temporary"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"oid":"42","coin":"BTC","side":"B","sz":"0.1","px":"30000","time":1700000000000}]`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unsupported request"}`))
		}
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	restClient := rest.New(srv.URL, 2*time.Second, zap.NewNop())
	wsURL := strings.Replace(srv.URL, "http", "ws", 1) + "/ws"
	wsClient := ws.New(wsURL, 10*time.Millisecond, 0, zap.NewNop())
	acct := account.New(restClient, wsClient, zap.NewNop(), "0xabc")
	if err := acct.Start(ctx); err != nil {
		t.Fatalf("account start: %v", err)
	}
	if !acct.FillsEnabled() {
		t.Fatalf("expected fills enabled")
	}

	app := &App{account: acct}
	startMS := time.Now().Add(-time.Second).UnixMilli()
	filled, open, err := app.waitForOrderFill(ctx, "42", startMS, 80*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForOrderFill: %v", err)
	}
	if open {
		t.Fatalf("expected open=false, got true")
	}
	if math.Abs(filled-0.1) > 1e-9 {
		t.Fatalf("expected filled=0.1, got %f", filled)
	}
	if got := userFillsCalls.Load(); got != 2 {
		t.Fatalf("expected 2 userFillsByTime calls, got %d", got)
	}
	select {
	case err := <-handlerErrCh:
		t.Fatalf("handler error: %v", err)
	default:
	}
}
