package account

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hl-carry-bot/internal/hl/rest"

	"go.uber.org/zap"
)

func TestUserFillsByTime(t *testing.T) {
	startMS := int64(1700000000000)
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			t.Fatalf("expected /info, got %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &gotPayload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"oid":123,"coin":"BTC","sz":"1.5","px":"30000","time":1700000000001}]`))
	}))
	defer server.Close()

	restClient := rest.New(server.URL, 5*time.Second, zap.NewNop())
	acct := New(restClient, nil, zap.NewNop(), "0xabc")
	fills, err := acct.UserFillsByTime(context.Background(), startMS, 0)
	if err != nil {
		t.Fatalf("user fills: %v", err)
	}
	if gotPayload["type"] != "userFillsByTime" {
		t.Fatalf("expected type userFillsByTime, got %v", gotPayload["type"])
	}
	if gotPayload["user"] != "0xabc" {
		t.Fatalf("expected user 0xabc, got %v", gotPayload["user"])
	}
	startVal, ok := gotPayload["startTime"].(float64)
	if !ok {
		t.Fatalf("expected startTime float64, got %T", gotPayload["startTime"])
	}
	if got := int64(startVal); got != startMS {
		t.Fatalf("expected startTime %d, got %d", startMS, got)
	}
	if len(fills) != 1 {
		t.Fatalf("expected 1 fill, got %d", len(fills))
	}
	if fills[0].OrderID != "123" {
		t.Fatalf("expected order id 123, got %s", fills[0].OrderID)
	}
	if fills[0].Size != 1.5 {
		t.Fatalf("expected size 1.5, got %f", fills[0].Size)
	}
}
