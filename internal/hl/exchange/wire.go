package exchange

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func LimitOrderWire(asset int, isBuy bool, size, limit float64, reduceOnly bool, tif Tif, cloid string) (OrderWire, error) {
	if tif == "" {
		return OrderWire{}, errors.New("tif is required")
	}
	price, err := floatToWire(limit)
	if err != nil {
		return OrderWire{}, fmt.Errorf("limit price: %w", err)
	}
	sizeWire, err := floatToWire(size)
	if err != nil {
		return OrderWire{}, fmt.Errorf("size: %w", err)
	}
	return OrderWire{
		Asset:      asset,
		IsBuy:      isBuy,
		Price:      price,
		Size:       sizeWire,
		ReduceOnly: reduceOnly,
		OrderType:  OrderTypeWire{Limit: &LimitOrderType{Tif: tif}},
		Cloid:      cloid,
	}, nil
}

func floatToWire(x float64) (string, error) {
	rounded := fmt.Sprintf("%.8f", x)
	parsed, err := strconv.ParseFloat(rounded, 64)
	if err != nil {
		return "", err
	}
	if math.Abs(parsed-x) >= 1e-12 {
		return "", fmt.Errorf("float_to_wire causes rounding: %f", x)
	}
	trimmed := strings.TrimRight(rounded, "0")
	trimmed = strings.TrimRight(trimmed, ".")
	if trimmed == "" || trimmed == "-0" {
		trimmed = "0"
	}
	return trimmed, nil
}
