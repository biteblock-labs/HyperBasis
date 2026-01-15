package alerts

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

	"hl-carry-bot/internal/config"

	"go.uber.org/zap"
)

const telegramBaseURL = "https://api.telegram.org"

type Telegram struct {
	enabled bool
	token   string
	chatID  string
	baseURL string
	client  *http.Client
	log     *zap.Logger
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

func NewTelegram(cfg config.TelegramConfig, log *zap.Logger) *Telegram {
	return newTelegram(cfg, log, telegramBaseURL, &http.Client{Timeout: 10 * time.Second})
}

func newTelegram(cfg config.TelegramConfig, log *zap.Logger, baseURL string, client *http.Client) *Telegram {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Telegram{
		enabled: cfg.Enabled,
		token:   strings.TrimSpace(cfg.Token),
		chatID:  strings.TrimSpace(cfg.ChatID),
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
		log:     log,
	}
}

func (t *Telegram) Send(ctx context.Context, message string) error {
	if !t.enabled {
		return nil
	}
	if t.token == "" || t.chatID == "" {
		return errors.New("telegram token and chat_id are required")
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("telegram message is empty")
	}
	payload := map[string]string{
		"chat_id": t.chatID,
		"text":    message,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram send failed: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
		if !result.OK {
			desc := strings.TrimSpace(result.Description)
			if desc == "" {
				desc = "unknown telegram error"
			}
			return fmt.Errorf("telegram send failed: %s", desc)
		}
	}
	return nil
}

func (t *Telegram) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	if t.token == "" {
		return nil, errors.New("telegram token is required")
	}
	payload := map[string]any{
		"allowed_updates": []string{"message"},
	}
	if offset > 0 {
		payload["offset"] = offset
	}
	if timeout > 0 {
		payload["timeout"] = int(timeout.Seconds())
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/bot%s/getUpdates", t.baseURL, t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("telegram getUpdates failed: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		OK          bool     `json:"ok"`
		Description string   `json:"description"`
		Result      []Update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		desc := strings.TrimSpace(result.Description)
		if desc == "" {
			desc = "unknown telegram error"
		}
		return nil, fmt.Errorf("telegram getUpdates failed: %s", desc)
	}
	return result.Result, nil
}
