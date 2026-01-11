package account

import (
	"context"
	"errors"
)

type Fill struct {
	OrderID string
	Asset   string
	Side    string
	Size    float64
	Price   float64
	TimeMS  int64
	Hash    string
}

func (a *Account) UserFillsByTime(ctx context.Context, startTimeMS, endTimeMS int64) ([]Fill, error) {
	if a.rest == nil {
		return nil, errors.New("rest client is required")
	}
	if a.user == "" {
		return nil, errors.New("account user is required")
	}
	if startTimeMS <= 0 {
		return nil, errors.New("start time must be > 0")
	}
	req := map[string]any{
		"type":      "userFillsByTime",
		"user":      a.user,
		"startTime": startTimeMS,
	}
	if endTimeMS > 0 {
		req["endTime"] = endTimeMS
	}
	resp, err := a.rest.InfoAny(ctx, req)
	if err != nil {
		return nil, err
	}
	return parseFills(resp), nil
}

func (a *Account) OpenOrders(ctx context.Context) ([]map[string]any, error) {
	if a.rest == nil {
		return nil, errors.New("rest client is required")
	}
	if a.user == "" {
		return nil, errors.New("account user is required")
	}
	resp, err := a.rest.InfoAny(ctx, map[string]any{
		"type": "openOrders",
		"user": a.user,
	})
	if err != nil {
		return nil, err
	}
	return parseOpenOrders(resp), nil
}

func parseFills(payload any) []Fill {
	if payload == nil {
		return nil
	}
	if list, ok := payload.([]map[string]any); ok {
		return parseFillListMaps(list)
	}
	if list, ok := payload.([]any); ok {
		return parseFillList(list)
	}
	if payloadMap, ok := payload.(map[string]any); ok {
		if list, ok := payloadMap["fills"].([]any); ok {
			return parseFillList(list)
		}
		if list, ok := payloadMap["data"].([]any); ok {
			return parseFillList(list)
		}
	}
	return nil
}

func parseFillList(raw []any) []Fill {
	if len(raw) == 0 {
		return nil
	}
	fills := make([]Fill, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fills = append(fills, parseFill(entry))
	}
	return fills
}

func parseFillListMaps(raw []map[string]any) []Fill {
	if len(raw) == 0 {
		return nil
	}
	fills := make([]Fill, 0, len(raw))
	for _, entry := range raw {
		fills = append(fills, parseFill(entry))
	}
	return fills
}

func parseFill(entry map[string]any) Fill {
	return Fill{
		OrderID: stringFromAny(entry["oid"]),
		Asset:   stringFromAny(entry["coin"]),
		Side:    stringFromAny(entry["side"]),
		Size:    floatOrZero(entry["sz"]),
		Price:   floatOrZero(entry["px"]),
		TimeMS:  int64FromAny(entry["time"]),
		Hash:    stringFromAny(entry["hash"]),
	}
}

func floatOrZero(v any) float64 {
	if f, ok := floatFromAny(v); ok {
		return f
	}
	return 0
}
