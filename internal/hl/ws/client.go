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
	log            *zap.Logger

	mu   sync.Mutex
	conn *websocket.Conn
	subs []interface{}
}

func New(url string, reconnectDelay time.Duration, log *zap.Logger) *Client {
	return &Client{url: url, reconnectDelay: reconnectDelay, log: log}
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
		if err := c.readLoop(ctx, handler); err != nil {
			c.log.Warn("ws read loop ended", zap.Error(err))
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
		if handler != nil {
			handler(json.RawMessage(data))
		}
	}
}

func (c *Client) resetConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close(websocket.StatusNormalClosure, "reset")
		c.conn = nil
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}
