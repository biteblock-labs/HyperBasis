package exchange

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/vmihailenco/msgpack/v5"
)

func TestFloatToWire(t *testing.T) {
	cases := []struct {
		in  float64
		out string
	}{
		{in: 1.23, out: "1.23"},
		{in: 0, out: "0"},
		{in: math.Copysign(0, -1), out: "0"},
		{in: 1.23000000, out: "1.23"},
	}
	for _, tc := range cases {
		got, err := floatToWire(tc.in)
		if err != nil {
			t.Fatalf("unexpected error for %f: %v", tc.in, err)
		}
		if got != tc.out {
			t.Fatalf("expected %s, got %s", tc.out, got)
		}
	}
	if _, err := floatToWire(1.234567891); err == nil {
		t.Fatalf("expected rounding error")
	}
}

func TestEncodeOrderActionDeterministic(t *testing.T) {
	order, err := LimitOrderWire(1, true, 2.5, 100.0, false, TifIoc, "")
	if err != nil {
		t.Fatalf("unexpected order wire error: %v", err)
	}
	action := OrderAction{Type: "order", Orders: []OrderWire{order}, Grouping: "na"}
	b1, err := EncodeOrderAction(action)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	b2, err := EncodeOrderAction(action)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("expected deterministic encoding")
	}
	var decoded map[string]any
	if err := msgpack.Unmarshal(b1, &decoded); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if decoded["type"] != "order" {
		t.Fatalf("unexpected action type")
	}
	orders, ok := decoded["orders"].([]any)
	if !ok || len(orders) != 1 {
		t.Fatalf("expected 1 order")
	}
	orderMap, ok := orders[0].(map[string]any)
	if !ok {
		t.Fatalf("expected order map")
	}
	if orderMap["p"] != "100" {
		t.Fatalf("expected price 100, got %v", orderMap["p"])
	}
	if orderMap["s"] != "2.5" {
		t.Fatalf("expected size 2.5, got %v", orderMap["s"])
	}
}

func TestSignerRecover(t *testing.T) {
	signer, err := NewSigner("4f3edf983ac636a65a842ce7c78d9aa706d3b113bce036f81af8f9b72d3d80b2", true)
	if err != nil {
		t.Fatalf("signer error: %v", err)
	}
	order, err := LimitOrderWire(1, true, 2.5, 100.0, false, TifIoc, "")
	if err != nil {
		t.Fatalf("order wire error: %v", err)
	}
	action := OrderAction{Type: "order", Orders: []OrderWire{order}, Grouping: "na"}
	nonce := uint64(1700000000000)
	sig, err := signer.SignOrderAction(action, nonce, nil, nil)
	if err != nil {
		t.Fatalf("sign error: %v", err)
	}
	payload, err := EncodeOrderAction(action)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	aHash := actionHash(payload, nonce, nil, nil)
	digest, err := typedDataHash(aHash, true)
	if err != nil {
		t.Fatalf("digest error: %v", err)
	}
	sigBytes, err := signatureBytes(sig)
	if err != nil {
		t.Fatalf("signature bytes error: %v", err)
	}
	pubKey, err := crypto.SigToPub(digest, sigBytes)
	if err != nil {
		t.Fatalf("recover error: %v", err)
	}
	recovered := crypto.PubkeyToAddress(*pubKey)
	if recovered != signer.Address() {
		t.Fatalf("expected %s, got %s", signer.Address().Hex(), recovered.Hex())
	}
}

func signatureBytes(sig Signature) ([]byte, error) {
	r, err := hexutil.Decode(sig.R)
	if err != nil {
		return nil, err
	}
	s, err := hexutil.Decode(sig.S)
	if err != nil {
		return nil, err
	}
	if len(r) != 32 || len(s) != 32 {
		return nil, errUnexpectedSigLen
	}
	v := sig.V - 27
	if v < 0 || v > 1 {
		return nil, errUnexpectedSigV
	}
	out := append(append([]byte{}, r...), s...)
	out = append(out, byte(v))
	return out, nil
}

var errUnexpectedSigLen = errors.New("unexpected signature length")
var errUnexpectedSigV = errors.New("unexpected signature v")
