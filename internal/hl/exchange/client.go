package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type Client struct {
	baseURL      string
	http         *http.Client
	signer       *Signer
	vaultAddress *common.Address
}

func NewClient(baseURL string, timeout time.Duration, signer *Signer, vaultAddress string) (*Client, error) {
	if signer == nil {
		return nil, errors.New("signer is required")
	}
	if baseURL == "" {
		baseURL = "https://api.hyperliquid.xyz"
	}
	var vault *common.Address
	if strings.TrimSpace(vaultAddress) != "" {
		addr := common.HexToAddress(vaultAddress)
		vault = &addr
	}
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: timeout,
		},
		signer:       signer,
		vaultAddress: vault,
	}, nil
}

func (c *Client) PlaceOrder(ctx context.Context, order OrderWire) (map[string]any, error) {
	action := OrderAction{Type: "order", Orders: []OrderWire{order}, Grouping: "na"}
	nonce := uint64(time.Now().UnixMilli())
	sig, err := c.signer.SignOrderAction(action, nonce, c.vaultAddress, nil)
	if err != nil {
		return nil, err
	}
	var vaultAddress *string
	if c.vaultAddress != nil {
		addr := c.vaultAddress.Hex()
		vaultAddress = &addr
	}
	payload := SignedAction{
		Action:       action,
		Nonce:        nonce,
		Signature:    sig,
		VaultAddress: vaultAddress,
		ExpiresAfter: nil,
	}
	return c.post(ctx, "/exchange", payload)
}

func (c *Client) post(ctx context.Context, path string, req any) (map[string]any, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := c.baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(payload))
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}
