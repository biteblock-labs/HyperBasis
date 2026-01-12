package exchange

import "strconv"

func OrderIDFromResponse(resp map[string]any) string {
	if resp == nil {
		return ""
	}
	return orderIDFromAny(resp)
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

func orderIDFromAny(v any) string {
	switch val := v.(type) {
	case map[string]any:
		for _, key := range []string{"orderId", "orderID", "oid", "id"} {
			if id := stringFromAny(val[key]); id != "" {
				return id
			}
		}
		for _, nested := range val {
			if id := orderIDFromAny(nested); id != "" {
				return id
			}
		}
	case []any:
		for _, nested := range val {
			if id := orderIDFromAny(nested); id != "" {
				return id
			}
		}
	}
	return ""
}
