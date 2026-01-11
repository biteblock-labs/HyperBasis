package market

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

func parsePerpContexts(payload any) (map[string]PerpContext, error) {
	universe, ctxs := extractUniverseAndCtxs(payload, "assetCtxs")
	if len(universe) == 0 || len(ctxs) == 0 {
		return nil, errors.New("metaAndAssetCtxs missing universe or asset contexts")
	}
	result := make(map[string]PerpContext)
	for i, entry := range universe {
		meta, ok := toMap(entry)
		if !ok {
			continue
		}
		name := stringFromMap(meta, "name", "coin", "symbol")
		if name == "" {
			continue
		}
		ctx, ok := indexedMap(ctxs, i)
		if !ok {
			continue
		}
		result[name] = PerpContext{
			Index:       intFromAny(meta["index"], i),
			FundingRate: floatFromMap(ctx, "funding", "fundingRate"),
			OraclePrice: floatFromMap(ctx, "oraclePx", "oraclePrice", "oracle"),
			MarkPrice:   floatFromMap(ctx, "markPx", "markPrice", "mark"),
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no perp contexts parsed")
	}
	return result, nil
}

func parseSpotContexts(payload any) (map[string]SpotContext, error) {
	universe, tokens := extractSpotUniverseAndTokens(payload)
	if len(universe) == 0 {
		return nil, errors.New("spot meta missing universe")
	}
	tokenMeta := tokenMetaByIndex(tokens)
	result := make(map[string]SpotContext)
	for i, entry := range universe {
		meta, ok := toMap(entry)
		if !ok {
			continue
		}
		rawName := stringFromMap(meta, "name", "symbol", "coin")
		base, quote, baseDecimals, quoteDecimals := baseQuoteFromTokens(meta, tokenMeta)
		name := spotSymbol(meta, base, quote)
		if name == "" {
			continue
		}
		midKey := rawName
		if midKey == "" {
			midKey = name
		}
		ctx := SpotContext{
			Symbol:          name,
			Base:            base,
			Quote:           quote,
			Index:           intFromAny(meta["index"], i),
			BaseSzDecimals:  baseDecimals,
			QuoteSzDecimals: quoteDecimals,
			RawName:         rawName,
			MidKey:          midKey,
		}
		result[name] = ctx
		if rawName != "" && rawName != name {
			result[rawName] = ctx
		}
		if ctx.Base != "" {
			if _, exists := result[ctx.Base]; !exists {
				result[ctx.Base] = ctx
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no spot contexts parsed")
	}
	return result, nil
}

func parseCandle(payload map[string]any) (string, float64, bool) {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return "", 0, false
	}
	asset := stringFromAny(data["coin"])
	if asset == "" {
		asset = stringFromAny(data["symbol"])
	}
	if asset == "" {
		asset = stringFromAny(data["asset"])
	}
	candle := data
	if nested, ok := data["candle"].(map[string]any); ok {
		candle = nested
	}
	close := floatFromMap(candle, "c", "close", "cls", "price")
	if asset == "" || close == 0 {
		return "", 0, false
	}
	return asset, close, true
}

func extractUniverseAndCtxs(payload any, ctxKey string) ([]any, []any) {
	if arr, ok := toSlice(payload); ok && len(arr) >= 2 {
		metaMap, _ := toMap(arr[0])
		if metaMap != nil {
			if universe, ok := toSlice(metaMap["universe"]); ok {
				ctxs, _ := toSlice(arr[1])
				return universe, ctxs
			}
		}
		if universe, ok := toSlice(arr[0]); ok {
			ctxs, _ := toSlice(arr[1])
			return universe, ctxs
		}
	}
	if metaMap, ok := toMap(payload); ok {
		universe, _ := toSlice(metaMap["universe"])
		ctxs, _ := toSlice(metaMap[ctxKey])
		if len(ctxs) == 0 {
			ctxs, _ = toSlice(metaMap["assetCtxs"])
		}
		return universe, ctxs
	}
	return nil, nil
}

func extractSpotUniverseAndTokens(payload any) ([]any, []any) {
	if arr, ok := toSlice(payload); ok && len(arr) >= 1 {
		metaMap, _ := toMap(arr[0])
		if metaMap != nil {
			universe, _ := toSlice(metaMap["universe"])
			tokens, _ := toSlice(metaMap["tokens"])
			return universe, tokens
		}
		if universe, ok := toSlice(arr[0]); ok {
			return universe, nil
		}
	}
	if metaMap, ok := toMap(payload); ok {
		universe, _ := toSlice(metaMap["universe"])
		tokens, _ := toSlice(metaMap["tokens"])
		return universe, tokens
	}
	return nil, nil
}

type tokenMeta struct {
	name       string
	szDecimals int
}

func tokenMetaByIndex(tokens []any) map[int]tokenMeta {
	if len(tokens) == 0 {
		return nil
	}
	names := make(map[int]tokenMeta, len(tokens))
	for i, item := range tokens {
		meta, ok := toMap(item)
		if !ok {
			continue
		}
		name := stringFromMap(meta, "name")
		if name == "" {
			continue
		}
		index := intFromAny(meta["index"], i)
		names[index] = tokenMeta{
			name:       name,
			szDecimals: intFromAny(meta["szDecimals"], -1),
		}
	}
	return names
}

func baseQuoteFromTokens(meta map[string]any, tokenNames map[int]tokenMeta) (string, string, int, int) {
	tokens, ok := toSlice(meta["tokens"])
	if !ok || len(tokens) < 2 || tokenNames == nil {
		return stringFromMap(meta, "base", "baseCoin"), stringFromMap(meta, "quote", "quoteCoin"), -1, -1
	}
	baseIdx := intFromAny(tokens[0], -1)
	quoteIdx := intFromAny(tokens[1], -1)
	base := tokenNames[baseIdx]
	quote := tokenNames[quoteIdx]
	return base.name, quote.name, base.szDecimals, quote.szDecimals
}

func spotSymbol(meta map[string]any, base, quote string) string {
	name := stringFromMap(meta, "name", "symbol", "coin")
	if name != "" && !strings.HasPrefix(name, "@") {
		return name
	}
	if base != "" && quote != "" {
		return base + "/" + quote
	}
	return strings.TrimSpace(name)
}

func indexedMap(items []any, idx int) (map[string]any, bool) {
	if idx < 0 || idx >= len(items) {
		return nil, false
	}
	return toMap(items[idx])
}

func toMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func toSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func stringFromMap(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s := stringFromAny(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func floatFromMap(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if f, ok := floatFromAny(v); ok {
				return f
			}
		}
	}
	return 0
}

func floatFromAny(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func intFromAny(v any, fallback int) int {
	if f, ok := floatFromAny(v); ok {
		return int(f)
	}
	return fallback
}
