package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

type Client struct {
	baseURL string
	http    *http.Client
	log     *zap.Logger
}

func New(baseURL string, timeout time.Duration, log *zap.Logger) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: timeout,
		},
		log: log,
	}
}

type InfoRequest struct {
	Type string `json:"type"`
}

type ExchangeRequest struct {
	Type  string      `json:"type"`
	Order interface{} `json:"order,omitempty"`
	Oid   string      `json:"oid,omitempty"`
}

type Order struct {
	Asset         int     `json:"asset"`
	IsBuy         bool    `json:"isBuy"`
	Size          float64 `json:"sz"`
	LimitPrice    float64 `json:"limitPx,omitempty"`
	ReduceOnly    bool    `json:"reduceOnly,omitempty"`
	ClientOrderID string  `json:"cloid,omitempty"`
}

func (c *Client) Info(ctx context.Context, req interface{}) (map[string]any, error) {
	return c.post(ctx, "/info", req)
}

func (c *Client) InfoAny(ctx context.Context, req interface{}) (any, error) {
	return c.postAny(ctx, "/info", req)
}

func (c *Client) Exchange(ctx context.Context, req interface{}) (map[string]any, error) {
	return c.post(ctx, "/exchange", req)
}

func (c *Client) PlaceOrder(ctx context.Context, order Order) (string, error) {
	resp, err := c.Exchange(ctx, ExchangeRequest{Type: "order", Order: order})
	if err != nil {
		return "", err
	}
	orderID := parseOrderID(resp)
	if orderID == "" {
		return "", errors.New("missing order id in exchange response")
	}
	return orderID, nil
}

func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	_, err := c.Exchange(ctx, ExchangeRequest{Type: "cancel", Oid: orderID})
	return err
}

func (c *Client) post(ctx context.Context, path string, req interface{}) (map[string]any, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := c.baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) postAny(ctx context.Context, path string, req interface{}) (any, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := c.baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	var data any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func parseOrderID(resp map[string]any) string {
	for _, key := range []string{"orderId", "orderID", "oid", "id"} {
		if v, ok := resp[key]; ok {
			switch val := v.(type) {
			case string:
				return val
			case float64:
				return fmt.Sprintf("%.0f", val)
			}
		}
	}
	return ""
}
