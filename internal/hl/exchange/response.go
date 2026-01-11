package exchange

import "strconv"

func OrderIDFromResponse(resp map[string]any) string {
	if resp == nil {
		return ""
	}
	for _, key := range []string{"orderId", "orderID", "oid", "id"} {
		if v, ok := resp[key]; ok {
			if id := stringFromAny(v); id != "" {
				return id
			}
		}
	}
	for _, key := range []string{"response", "data"} {
		if nested, ok := resp[key].(map[string]any); ok {
			if id := OrderIDFromResponse(nested); id != "" {
				return id
			}
		}
	}
	return ""
}

func stringFromAny(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatInt(int64(val), 10)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	default:
		return ""
	}
}
