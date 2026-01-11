package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"
)

type Client struct {
	url            string
	reconnectDelay time.Duration
	pingInterval   time.Duration
	log            *zap.Logger

	mu   sync.Mutex
	conn *websocket.Conn
	subs []interface{}

	postMu  sync.Mutex
	postReq map[uint64]chan json.RawMessage
}

func New(url string, reconnectDelay, pingInterval time.Duration, log *zap.Logger) *Client {
	return &Client{url: url, reconnectDelay: reconnectDelay, pingInterval: pingInterval, log: log}
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	conn, _, err := websocket.Dial(ctx, c.url, nil)
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

func (c *Client) Subscribe(ctx context.Context, sub interface{}) error {
	c.mu.Lock()
	c.subs = append(c.subs, sub)
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("ws not connected")
	}
	return writeJSON(ctx, conn, sub)
}

func (c *Client) Run(ctx context.Context, handler func(json.RawMessage)) error {
	for {
		if err := c.ensureConnected(ctx); err != nil {
			return err
		}
		pingCtx, cancel := context.WithCancel(ctx)
		pingDone := make(chan struct{})
		go func() {
			defer close(pingDone)
			c.pingLoop(pingCtx)
		}()
		err := c.readLoop(ctx, handler)
		cancel()
		<-pingDone
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logReadLoopError(err)
			c.resetConn()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.reconnectDelay):
			}
			continue
		}
	}
}

func (c *Client) ensureConnected(ctx context.Context) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	conn := c.conn
	subs := append([]interface{}(nil), c.subs...)
	c.mu.Unlock()
	for _, sub := range subs {
		if err := writeJSON(ctx, conn, sub); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) readLoop(ctx context.Context, handler func(json.RawMessage)) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("ws not connected")
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if c.handlePostResponse(data) {
			continue
		}
		if handler != nil {
			handler(json.RawMessage(data))
		}
	}
}

func (c *Client) pingLoop(ctx context.Context) {
	c.mu.Lock()
	conn := c.conn
	interval := c.pingInterval
	c.mu.Unlock()
	if conn == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := writeJSON(ctx, conn, pingMessage); err != nil {
				return
			}
		}
	}
}

func (c *Client) logReadLoopError(err error) {
	if c.log == nil {
		return
	}
	status := websocket.CloseStatus(err)
	if status == websocket.StatusNormalClosure {
		var closeErr websocket.CloseError
		if errors.As(err, &closeErr) {
			c.log.Info("ws read loop ended", zap.Int("status", int(closeErr.Code)), zap.String("reason", closeErr.Reason))
			return
		}
		c.log.Info("ws read loop ended", zap.Error(err))
		return
	}
	c.log.Warn("ws read loop ended", zap.Error(err))
}

func (c *Client) resetConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close(websocket.StatusNormalClosure, "reset")
		c.conn = nil
	}
}

func (c *Client) Post(ctx context.Context, id uint64, req interface{}) (json.RawMessage, error) {
	if id == 0 {
		return nil, errors.New("post id is required")
	}
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("ws not connected")
	}
	respCh := make(chan json.RawMessage, 1)
	c.postMu.Lock()
	if c.postReq == nil {
		c.postReq = make(map[uint64]chan json.RawMessage)
	}
	if _, exists := c.postReq[id]; exists {
		c.postMu.Unlock()
		return nil, errors.New("post id already in use")
	}
	c.postReq[id] = respCh
	c.postMu.Unlock()

	payload := map[string]any{
		"method":  "post",
		"id":      id,
		"request": req,
	}
	if err := writeJSON(ctx, conn, payload); err != nil {
		c.removePostWaiter(id)
		return nil, err
	}
	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		c.removePostWaiter(id)
		return nil, ctx.Err()
	}
}

func (c *Client) handlePostResponse(data []byte) bool {
	c.postMu.Lock()
	hasPending := len(c.postReq) > 0
	c.postMu.Unlock()
	if !hasPending {
		return false
	}
	var payload struct {
		Channel string `json:"channel"`
		Data    struct {
			ID uint64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	if payload.Channel != "post" || payload.Data.ID == 0 {
		return false
	}
	c.postMu.Lock()
	waiter, ok := c.postReq[payload.Data.ID]
	if ok {
		delete(c.postReq, payload.Data.ID)
	}
	c.postMu.Unlock()
	if !ok {
		return false
	}
	waiter <- json.RawMessage(data)
	close(waiter)
	return true
}

func (c *Client) removePostWaiter(id uint64) {
	c.postMu.Lock()
	defer c.postMu.Unlock()
	if waiter, ok := c.postReq[id]; ok {
		delete(c.postReq, id)
		close(waiter)
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

var pingMessage = map[string]any{"method": "ping"}
