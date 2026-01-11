package alerts

import (
	"context"

	"go.uber.org/zap"
)

type Telegram struct {
	enabled bool
	log     *zap.Logger
}

func NewTelegram(enabled bool, log *zap.Logger) *Telegram {
	return &Telegram{enabled: enabled, log: log}
}

func (t *Telegram) Send(ctx context.Context, message string) error {
	if !t.enabled {
		return nil
	}
	_ = ctx
	t.log.Info("telegram alert", zap.String("message", message))
	return nil
}
