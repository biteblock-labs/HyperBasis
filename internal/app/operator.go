package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"hl-carry-bot/internal/alerts"
	"hl-carry-bot/internal/config"

	"go.uber.org/zap"
)

const operatorOffsetKey = "telegram:operator:last_update_id"

type operatorMeta struct {
	UpdateID int64
	UserID   int64
	Username string
	ChatID   int64
	Raw      string
}

type operatorAuditEvent struct {
	UpdateID     int64              `json:"update_id"`
	Time         time.Time          `json:"time"`
	Action       string             `json:"action"`
	Command      string             `json:"command"`
	UserID       int64              `json:"user_id"`
	Username     string             `json:"username,omitempty"`
	ChatID       int64              `json:"chat_id"`
	PausedBefore bool               `json:"paused_before"`
	PausedAfter  bool               `json:"paused_after"`
	RiskBefore   *config.RiskConfig `json:"risk_before,omitempty"`
	RiskAfter    *config.RiskConfig `json:"risk_after,omitempty"`
}

func (a *App) startOperator(ctx context.Context) {
	if a.cfg == nil || a.alerts == nil || a.log == nil {
		return
	}
	if !a.cfg.Telegram.OperatorEnabled {
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(a.cfg.Telegram.ChatID), 10, 64)
	if err != nil {
		a.log.Warn("telegram operator disabled: invalid chat_id", zap.Error(err))
		return
	}
	pollInterval := a.cfg.Telegram.OperatorPollInterval
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	allowedUsers := make(map[int64]struct{}, len(a.cfg.Telegram.OperatorAllowedUserIDs))
	for _, id := range a.cfg.Telegram.OperatorAllowedUserIDs {
		allowedUsers[id] = struct{}{}
	}
	go a.operatorLoop(ctx, chatID, allowedUsers, pollInterval)
}

func (a *App) operatorLoop(ctx context.Context, chatID int64, allowedUsers map[int64]struct{}, pollInterval time.Duration) {
	offset := a.loadOperatorOffset(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, err := a.alerts.GetUpdates(ctx, offset, pollInterval)
		if err != nil {
			a.logOperatorError(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollInterval):
			}
			continue
		}
		if a.operatorWarned {
			a.log.Info("telegram operator recovered")
			a.operatorWarned = false
		}
		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
				a.saveOperatorOffset(ctx, offset)
			}
			a.handleOperatorUpdate(ctx, upd, chatID, allowedUsers)
		}
	}
}

func (a *App) handleOperatorUpdate(ctx context.Context, upd alerts.Update, chatID int64, allowedUsers map[int64]struct{}) {
	if upd.Message == nil {
		return
	}
	msg := upd.Message
	if msg.Chat == nil || msg.From == nil {
		return
	}
	if msg.Chat.ID != chatID {
		return
	}
	if len(allowedUsers) > 0 {
		if _, ok := allowedUsers[msg.From.ID]; !ok {
			return
		}
	}
	cmd, args, ok := parseOperatorCommand(msg.Text)
	if !ok {
		return
	}
	meta := operatorMeta{
		UpdateID: upd.UpdateID,
		UserID:   msg.From.ID,
		Username: msg.From.Username,
		ChatID:   msg.Chat.ID,
		Raw:      msg.Text,
	}
	resp, err := a.handleOperatorCommand(ctx, cmd, args, meta)
	if err != nil {
		resp = fmt.Sprintf("command failed: %v", err)
	}
	if resp == "" {
		return
	}
	if err := a.alerts.Send(ctx, resp); err != nil {
		a.log.Warn("operator response failed", zap.Error(err))
	}
}

func parseOperatorCommand(text string) (string, []string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", nil, false
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", nil, false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", nil, false
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	return cmd, fields[1:], true
}

func (a *App) handleOperatorCommand(ctx context.Context, cmd string, args []string, meta operatorMeta) (string, error) {
	switch cmd {
	case "status":
		return a.operatorStatus(ctx), nil
	case "pause":
		before := a.isPaused()
		after := a.setPaused(true)
		a.auditOperatorEvent(ctx, operatorAuditEvent{
			UpdateID:     meta.UpdateID,
			Time:         time.Now().UTC(),
			Action:       "pause",
			Command:      meta.Raw,
			UserID:       meta.UserID,
			Username:     meta.Username,
			ChatID:       meta.ChatID,
			PausedBefore: before,
			PausedAfter:  after,
		})
		if after {
			return "trading paused", nil
		}
		return "trading already paused", nil
	case "resume":
		before := a.isPaused()
		after := a.setPaused(false)
		a.auditOperatorEvent(ctx, operatorAuditEvent{
			UpdateID:     meta.UpdateID,
			Time:         time.Now().UTC(),
			Action:       "resume",
			Command:      meta.Raw,
			UserID:       meta.UserID,
			Username:     meta.Username,
			ChatID:       meta.ChatID,
			PausedBefore: before,
			PausedAfter:  after,
		})
		if !after {
			return "trading resumed", nil
		}
		return "trading already active", nil
	case "risk":
		return a.handleRiskCommand(ctx, args, meta)
	case "help":
		return operatorHelpText(), nil
	default:
		return operatorHelpText(), nil
	}
}

func (a *App) handleRiskCommand(ctx context.Context, args []string, meta operatorMeta) (string, error) {
	if len(args) == 0 || strings.EqualFold(args[0], "show") {
		return a.riskStatus(), nil
	}
	switch strings.ToLower(args[0]) {
	case "reset":
		before := a.riskOverrideSnapshot()
		a.clearRiskOverride()
		a.auditOperatorEvent(ctx, operatorAuditEvent{
			UpdateID:   meta.UpdateID,
			Time:       time.Now().UTC(),
			Action:     "risk_reset",
			Command:    meta.Raw,
			UserID:     meta.UserID,
			Username:   meta.Username,
			ChatID:     meta.ChatID,
			RiskBefore: before,
		})
		return "risk override cleared", nil
	case "set":
		overrides, err := parseRiskOverrides(args[1:])
		if err != nil {
			return "", err
		}
		before := a.riskOverrideSnapshot()
		base := a.riskConfig()
		next, err := applyRiskOverrides(base, overrides)
		if err != nil {
			return "", err
		}
		if riskConfigsEqual(next, a.cfg.Risk) {
			a.clearRiskOverride()
		} else {
			a.setRiskOverride(next)
		}
		after := a.riskOverrideSnapshot()
		a.auditOperatorEvent(ctx, operatorAuditEvent{
			UpdateID:   meta.UpdateID,
			Time:       time.Now().UTC(),
			Action:     "risk_set",
			Command:    meta.Raw,
			UserID:     meta.UserID,
			Username:   meta.Username,
			ChatID:     meta.ChatID,
			RiskBefore: before,
			RiskAfter:  after,
		})
		return "risk override updated", nil
	default:
		return "", errors.New("unknown risk command: use /risk show|set|reset")
	}
}

func parseRiskOverrides(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return nil, errors.New("risk set requires key=value pairs")
	}
	out := make(map[string]string)
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid risk setting: %s", arg)
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])
		if key == "" || val == "" {
			return nil, fmt.Errorf("invalid risk setting: %s", arg)
		}
		out[key] = val
	}
	return out, nil
}

func applyRiskOverrides(base config.RiskConfig, overrides map[string]string) (config.RiskConfig, error) {
	next := base
	for key, val := range overrides {
		switch key {
		case "max_notional_usd":
			parsed, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return config.RiskConfig{}, fmt.Errorf("max_notional_usd: %w", err)
			}
			next.MaxNotionalUSD = parsed
		case "max_open_orders":
			parsed, err := strconv.Atoi(val)
			if err != nil {
				return config.RiskConfig{}, fmt.Errorf("max_open_orders: %w", err)
			}
			next.MaxOpenOrders = parsed
		case "min_margin_ratio":
			parsed, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return config.RiskConfig{}, fmt.Errorf("min_margin_ratio: %w", err)
			}
			next.MinMarginRatio = parsed
		case "min_health_ratio":
			parsed, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return config.RiskConfig{}, fmt.Errorf("min_health_ratio: %w", err)
			}
			next.MinHealthRatio = parsed
		case "max_market_age":
			dur, err := time.ParseDuration(val)
			if err != nil {
				return config.RiskConfig{}, fmt.Errorf("max_market_age: %w", err)
			}
			next.MaxMarketAge = dur
		case "max_account_age":
			dur, err := time.ParseDuration(val)
			if err != nil {
				return config.RiskConfig{}, fmt.Errorf("max_account_age: %w", err)
			}
			next.MaxAccountAge = dur
		default:
			return config.RiskConfig{}, fmt.Errorf("unknown risk key: %s", key)
		}
	}
	if err := validateRiskOverride(next); err != nil {
		return config.RiskConfig{}, err
	}
	return next, nil
}

func validateRiskOverride(risk config.RiskConfig) error {
	if risk.MaxNotionalUSD < 0 {
		return errors.New("max_notional_usd must be >= 0")
	}
	if risk.MaxOpenOrders < 0 {
		return errors.New("max_open_orders must be >= 0")
	}
	if risk.MinMarginRatio < 0 {
		return errors.New("min_margin_ratio must be >= 0")
	}
	if risk.MinHealthRatio < 0 {
		return errors.New("min_health_ratio must be >= 0")
	}
	if risk.MaxMarketAge < 0 {
		return errors.New("max_market_age must be >= 0")
	}
	if risk.MaxAccountAge < 0 {
		return errors.New("max_account_age must be >= 0")
	}
	return nil
}

func (a *App) operatorStatus(ctx context.Context) string {
	if a.cfg == nil {
		return "status unavailable"
	}
	state := "unknown"
	if a.strategy != nil {
		state = string(a.strategy.State)
	}
	accountSnap := a.account.Snapshot()
	spotBalance := a.spotBalanceForAsset(a.cfg.Strategy.SpotAsset, accountSnap.SpotBalances)
	perpPosition := accountSnap.PerpPosition[a.cfg.Strategy.PerpAsset]
	spotMid, _, _ := a.spotMid(ctx, a.cfg.Strategy.SpotAsset)
	perpMid, _ := a.market.Mid(ctx, a.cfg.Strategy.PerpAsset)
	oraclePrice, _ := a.market.OraclePrice(a.cfg.Strategy.PerpAsset)
	fundingRate, _ := a.market.FundingRate(a.cfg.Strategy.PerpAsset)
	priceRef := oraclePrice
	if priceRef == 0 {
		priceRef = perpMid
	}
	if priceRef == 0 {
		priceRef = spotMid
	}
	deltaUSD := (spotBalance + perpPosition) * priceRef
	forecast, hasForecast := a.market.FundingForecast(a.cfg.Strategy.PerpAsset)
	nextFunding := "n/a"
	if hasForecast && forecast.HasNext {
		nextFunding = forecast.NextFunding.UTC().Format(time.RFC3339)
	}
	paused := a.isPaused()
	entryCooldownActive := a.entryCooldownActive(time.Now().UTC())
	hedgeCooldownActive := a.hedgeCooldownActive(time.Now().UTC())
	riskOverride := a.riskOverrideActive()
	lastFunding := "n/a"
	if !a.lastFundingReceiptAt.IsZero() {
		lastFunding = a.lastFundingReceiptAt.UTC().Format(time.RFC3339)
	}
	return strings.Join([]string{
		fmt.Sprintf("state: %s", state),
		fmt.Sprintf("paused: %t", paused),
		fmt.Sprintf("spot_balance: %.6f %s", spotBalance, a.cfg.Strategy.SpotAsset),
		fmt.Sprintf("perp_position: %.6f %s", perpPosition, a.cfg.Strategy.PerpAsset),
		fmt.Sprintf("delta_usd: %.4f (band %.2f)", deltaUSD, a.cfg.Strategy.DeltaBandUSD),
		fmt.Sprintf("funding_rate: %.8f", fundingRate),
		fmt.Sprintf("next_funding_at: %s", nextFunding),
		fmt.Sprintf("entry_cooldown_active: %t", entryCooldownActive),
		fmt.Sprintf("hedge_cooldown_active: %t", hedgeCooldownActive),
		fmt.Sprintf("risk_override_active: %t", riskOverride),
		fmt.Sprintf("last_funding_receipt: %s", lastFunding),
	}, "\n")
}

func (a *App) riskStatus() string {
	effective := a.riskConfig()
	override := a.riskOverrideSnapshot()
	lines := []string{
		fmt.Sprintf("risk effective: max_notional_usd=%.2f max_open_orders=%d min_margin_ratio=%.4f min_health_ratio=%.4f max_market_age=%s max_account_age=%s",
			effective.MaxNotionalUSD,
			effective.MaxOpenOrders,
			effective.MinMarginRatio,
			effective.MinHealthRatio,
			effective.MaxMarketAge,
			effective.MaxAccountAge,
		),
	}
	if override != nil {
		lines = append(lines, fmt.Sprintf("risk override: max_notional_usd=%.2f max_open_orders=%d min_margin_ratio=%.4f min_health_ratio=%.4f max_market_age=%s max_account_age=%s",
			override.MaxNotionalUSD,
			override.MaxOpenOrders,
			override.MinMarginRatio,
			override.MinHealthRatio,
			override.MaxMarketAge,
			override.MaxAccountAge,
		))
	} else {
		lines = append(lines, "risk override: none")
	}
	return strings.Join(lines, "\n")
}

func operatorHelpText() string {
	return strings.Join([]string{
		"commands:",
		"/status - current bot status",
		"/pause - pause new trading actions",
		"/resume - resume trading actions",
		"/risk show - show active risk settings",
		"/risk set key=value ... - override risk (keys: max_notional_usd, max_open_orders, min_margin_ratio, min_health_ratio, max_market_age, max_account_age)",
		"/risk reset - clear risk override",
	}, "\n")
}

func (a *App) isPaused() bool {
	a.opsMu.RLock()
	defer a.opsMu.RUnlock()
	return a.paused
}

func (a *App) setPaused(paused bool) bool {
	a.opsMu.Lock()
	defer a.opsMu.Unlock()
	a.paused = paused
	return a.paused
}

func (a *App) riskConfig() config.RiskConfig {
	a.opsMu.RLock()
	override := a.riskOverride
	a.opsMu.RUnlock()
	if override == nil {
		return a.cfg.Risk
	}
	return *override
}

func (a *App) riskOverrideActive() bool {
	a.opsMu.RLock()
	defer a.opsMu.RUnlock()
	return a.riskOverride != nil
}

func (a *App) riskOverrideSnapshot() *config.RiskConfig {
	a.opsMu.RLock()
	defer a.opsMu.RUnlock()
	if a.riskOverride == nil {
		return nil
	}
	copy := *a.riskOverride
	return &copy
}

func (a *App) setRiskOverride(risk config.RiskConfig) {
	a.opsMu.Lock()
	defer a.opsMu.Unlock()
	a.riskOverride = &risk
}

func (a *App) clearRiskOverride() {
	a.opsMu.Lock()
	defer a.opsMu.Unlock()
	a.riskOverride = nil
}

func (a *App) logOperatorError(err error) {
	if a.log == nil {
		return
	}
	if a.operatorWarned {
		return
	}
	a.operatorWarned = true
	a.log.Warn("telegram operator failed", zap.Error(err))
}

func (a *App) loadOperatorOffset(ctx context.Context) int64 {
	if a.store == nil {
		return 0
	}
	raw, ok, err := a.store.Get(ctx, operatorOffsetKey)
	if err != nil || !ok {
		return 0
	}
	val, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	if val < 0 {
		return 0
	}
	return val
}

func (a *App) saveOperatorOffset(ctx context.Context, offset int64) {
	if a.store == nil {
		return
	}
	_ = a.store.Set(ctx, operatorOffsetKey, strconv.FormatInt(offset, 10))
}

func (a *App) auditOperatorEvent(ctx context.Context, event operatorAuditEvent) {
	if a.store == nil {
		return
	}
	key := fmt.Sprintf("ops:audit:%d:%d", time.Now().UTC().UnixNano(), event.UpdateID)
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_ = a.store.Set(ctx, key, string(payload))
}

func riskConfigsEqual(aCfg config.RiskConfig, bCfg config.RiskConfig) bool {
	return aCfg.MaxNotionalUSD == bCfg.MaxNotionalUSD &&
		aCfg.MaxOpenOrders == bCfg.MaxOpenOrders &&
		aCfg.MinMarginRatio == bCfg.MinMarginRatio &&
		aCfg.MinHealthRatio == bCfg.MinHealthRatio &&
		aCfg.MaxMarketAge == bCfg.MaxMarketAge &&
		aCfg.MaxAccountAge == bCfg.MaxAccountAge
}
