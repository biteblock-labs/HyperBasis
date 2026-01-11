package exchange

import (
	"bytes"
	"errors"

	"github.com/vmihailenco/msgpack/v5"
)

func EncodeOrderAction(action OrderAction) ([]byte, error) {
	if action.Type == "" {
		return nil, errors.New("action type is required")
	}
	if len(action.Orders) == 0 {
		return nil, errors.New("action orders are required")
	}
	if action.Grouping == "" {
		action.Grouping = "na"
	}
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	mapLen := 3
	if action.Builder != nil {
		mapLen++
	}
	if err := enc.EncodeMapLen(mapLen); err != nil {
		return nil, err
	}
	if err := enc.EncodeString("type"); err != nil {
		return nil, err
	}
	if err := enc.EncodeString(action.Type); err != nil {
		return nil, err
	}
	if err := enc.EncodeString("orders"); err != nil {
		return nil, err
	}
	if err := enc.EncodeArrayLen(len(action.Orders)); err != nil {
		return nil, err
	}
	for _, order := range action.Orders {
		if err := encodeOrderWire(enc, order); err != nil {
			return nil, err
		}
	}
	if err := enc.EncodeString("grouping"); err != nil {
		return nil, err
	}
	if err := enc.EncodeString(action.Grouping); err != nil {
		return nil, err
	}
	if action.Builder != nil {
		if err := enc.EncodeString("builder"); err != nil {
			return nil, err
		}
		if err := enc.Encode(action.Builder); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func EncodeCancelAction(action CancelAction) ([]byte, error) {
	if action.Type == "" {
		return nil, errors.New("action type is required")
	}
	if len(action.Cancels) == 0 {
		return nil, errors.New("action cancels are required")
	}
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	if err := enc.EncodeMapLen(2); err != nil {
		return nil, err
	}
	if err := enc.EncodeString("type"); err != nil {
		return nil, err
	}
	if err := enc.EncodeString(action.Type); err != nil {
		return nil, err
	}
	if err := enc.EncodeString("cancels"); err != nil {
		return nil, err
	}
	if err := enc.EncodeArrayLen(len(action.Cancels)); err != nil {
		return nil, err
	}
	for _, cancel := range action.Cancels {
		if err := encodeCancelWire(enc, cancel); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func encodeOrderWire(enc *msgpack.Encoder, order OrderWire) error {
	mapLen := 6
	if order.Cloid != "" {
		mapLen++
	}
	if err := enc.EncodeMapLen(mapLen); err != nil {
		return err
	}
	if err := enc.EncodeString("a"); err != nil {
		return err
	}
	if err := enc.EncodeInt(int64(order.Asset)); err != nil {
		return err
	}
	if err := enc.EncodeString("b"); err != nil {
		return err
	}
	if err := enc.EncodeBool(order.IsBuy); err != nil {
		return err
	}
	if err := enc.EncodeString("p"); err != nil {
		return err
	}
	if err := enc.EncodeString(order.Price); err != nil {
		return err
	}
	if err := enc.EncodeString("s"); err != nil {
		return err
	}
	if err := enc.EncodeString(order.Size); err != nil {
		return err
	}
	if err := enc.EncodeString("r"); err != nil {
		return err
	}
	if err := enc.EncodeBool(order.ReduceOnly); err != nil {
		return err
	}
	if err := enc.EncodeString("t"); err != nil {
		return err
	}
	if err := encodeOrderTypeWire(enc, order.OrderType); err != nil {
		return err
	}
	if order.Cloid != "" {
		if err := enc.EncodeString("c"); err != nil {
			return err
		}
		if err := enc.EncodeString(order.Cloid); err != nil {
			return err
		}
	}
	return nil
}

func encodeCancelWire(enc *msgpack.Encoder, cancel CancelWire) error {
	if err := enc.EncodeMapLen(2); err != nil {
		return err
	}
	if err := enc.EncodeString("a"); err != nil {
		return err
	}
	if err := enc.EncodeInt(int64(cancel.Asset)); err != nil {
		return err
	}
	if err := enc.EncodeString("o"); err != nil {
		return err
	}
	return enc.EncodeInt(cancel.OrderID)
}

func encodeOrderTypeWire(enc *msgpack.Encoder, orderType OrderTypeWire) error {
	if orderType.Limit == nil {
		return errors.New("limit order type required")
	}
	if err := enc.EncodeMapLen(1); err != nil {
		return err
	}
	if err := enc.EncodeString("limit"); err != nil {
		return err
	}
	if err := enc.EncodeMapLen(1); err != nil {
		return err
	}
	if err := enc.EncodeString("tif"); err != nil {
		return err
	}
	return enc.EncodeString(string(orderType.Limit.Tif))
}
