package exchange

import "testing"

func TestOrderIDFromResponseStatusFilled(t *testing.T) {
	resp := map[string]any{
		"status": "ok",
		"response": map[string]any{
			"type": "order",
			"data": map[string]any{
				"statuses": []any{
					map[string]any{
						"filled": map[string]any{
							"oid":   float64(292577153770),
							"cloid": "0x188a0f9ee162351d6d6af5b09b97b1c7",
						},
					},
				},
			},
		},
	}
	got := OrderIDFromResponse(resp)
	if got != "292577153770" {
		t.Fatalf("expected order id 292577153770, got %s", got)
	}
}
