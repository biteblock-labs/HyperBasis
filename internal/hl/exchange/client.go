package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.uber.org/zap"
)

type Client struct {
	baseURL       string
	http          *http.Client
	signer        *Signer
	vaultAddress  *common.Address
	lastNonce     atomic.Uint64
	lastPersisted atomic.Uint64
	nonceStore    NonceStore
	nonceKey      string
	log           *zap.Logger
	persistMu     sync.Mutex
	persistWarned atomic.Bool
}

type NonceStore interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

type NonceState struct {
	Key       string
	Last      uint64
	Persisted uint64
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

func (c *Client) SetLogger(log *zap.Logger) {
	c.log = log
}

func (c *Client) PlaceOrder(ctx context.Context, order OrderWire) (map[string]any, error) {
	action := OrderAction{Type: "order", Orders: []OrderWire{order}, Grouping: "na"}
	nonce := c.nextNonce()
	sig, err := c.signer.SignOrderAction(action, nonce, c.vaultAddress, nil)
	if err != nil {
		return nil, err
	}
	return c.postAction(ctx, action, sig, nonce, true)
}

func (c *Client) CancelOrder(ctx context.Context, asset int, orderID int64) (map[string]any, error) {
	action := CancelAction{Type: "cancel", Cancels: []CancelWire{{Asset: asset, OrderID: orderID}}}
	nonce := c.nextNonce()
	sig, err := c.signer.SignCancelAction(action, nonce, c.vaultAddress, nil)
	if err != nil {
		return nil, err
	}
	return c.postAction(ctx, action, sig, nonce, true)
}

func (c *Client) USDClassTransfer(ctx context.Context, amount float64, toPerp bool) (map[string]any, error) {
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	amountStr := strconv.FormatFloat(amount, 'f', -1, 64)
	if c.vaultAddress != nil {
		amountStr += " subaccount:" + c.vaultAddress.Hex()
	}
	nonce := c.nextNonce()
	action := USDClassTransferAction{
		Type:   "usdClassTransfer",
		Amount: amountStr,
		ToPerp: toPerp,
		Nonce:  nonce,
	}
	sig, err := c.signer.SignUSDClassTransfer(&action)
	if err != nil {
		return nil, err
	}
	return c.postAction(ctx, action, sig, action.Nonce, false)
}

func (c *Client) InitNonceStore(ctx context.Context, store NonceStore) error {
	if store == nil {
		return nil
	}
	if c.signer == nil {
		return errors.New("signer is required for nonce store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := nonceStoreKey(c.baseURL, c.signer, c.vaultAddress)
	now := uint64(time.Now().UnixMilli())
	seed := now
	if raw, ok, err := store.Get(ctx, key); err != nil {
		return err
	} else if ok {
		parsed, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid stored nonce %q: %w", raw, err)
		}
		if parsed > seed {
			seed = parsed
		}
	}
	if current := c.lastNonce.Load(); current > seed {
		seed = current
	}
	c.nonceStore = store
	c.nonceKey = key
	c.lastNonce.Store(seed)
	c.lastPersisted.Store(seed)
	return nil
}

func (c *Client) NonceState() (NonceState, bool) {
	if c.nonceStore == nil || c.nonceKey == "" {
		return NonceState{}, false
	}
	return NonceState{
		Key:       c.nonceKey,
		Last:      c.lastNonce.Load(),
		Persisted: c.lastPersisted.Load(),
	}, true
}

func (c *Client) nextNonce() uint64 {
	now := uint64(time.Now().UnixMilli())
	for {
		prev := c.lastNonce.Load()
		next := now
		if prev >= next {
			next = prev + 1
		}
		if c.lastNonce.CompareAndSwap(prev, next) {
			c.persistNonce(next)
			return next
		}
	}
}

func (c *Client) persistNonce(nonce uint64) {
	if c.nonceStore == nil || c.nonceKey == "" {
		return
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	if nonce <= c.lastPersisted.Load() {
		return
	}
	if err := c.nonceStore.Set(context.Background(), c.nonceKey, strconv.FormatUint(nonce, 10)); err != nil {
		c.logPersistError(err)
		return
	}
	c.lastPersisted.Store(nonce)
	c.persistWarned.Store(false)
}

func (c *Client) logPersistError(err error) {
	if c.log == nil {
		return
	}
	if c.persistWarned.CompareAndSwap(false, true) {
		c.log.Warn("nonce persistence failed", zap.String("nonce_key", c.nonceKey), zap.Error(err))
	}
}

func nonceStoreKey(baseURL string, signer *Signer, vaultAddress *common.Address) string {
	addr := "unknown"
	if signer != nil {
		addr = strings.ToLower(signer.Address().Hex())
	}
	vault := "none"
	if vaultAddress != nil {
		vault = strings.ToLower(vaultAddress.Hex())
	}
	return fmt.Sprintf("exchange:nonce:%s:%s:%s", strings.ToLower(strings.TrimSpace(baseURL)), addr, vault)
}

func (c *Client) postAction(ctx context.Context, action any, sig Signature, nonce uint64, includeVault bool) (map[string]any, error) {
	var vaultAddress *string
	if includeVault && c.vaultAddress != nil {
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
