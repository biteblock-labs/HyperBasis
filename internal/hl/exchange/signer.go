package exchange

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

type Signer struct {
	privKey   *ecdsa.PrivateKey
	address   common.Address
	isMainnet bool
}

func NewSigner(hexKey string, isMainnet bool) (*Signer, error) {
	clean := strings.TrimSpace(hexKey)
	if clean == "" {
		return nil, errors.New("private key is required")
	}
	clean = strings.TrimPrefix(clean, "0x")
	key, err := crypto.HexToECDSA(clean)
	if err != nil {
		return nil, err
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	return &Signer{privKey: key, address: addr, isMainnet: isMainnet}, nil
}

func (s *Signer) Address() common.Address {
	return s.address
}

func (s *Signer) SignOrderAction(action OrderAction, nonce uint64, vaultAddress *common.Address, expiresAfter *uint64) (Signature, error) {
	payload, err := EncodeOrderAction(action)
	if err != nil {
		return Signature{}, err
	}
	hash := actionHash(payload, nonce, vaultAddress, expiresAfter)
	digest, err := typedDataHash(hash, s.isMainnet)
	if err != nil {
		return Signature{}, err
	}
	sig, err := crypto.Sign(digest, s.privKey)
	if err != nil {
		return Signature{}, err
	}
	return signatureFromBytes(sig)
}

func (s *Signer) SignCancelAction(action CancelAction, nonce uint64, vaultAddress *common.Address, expiresAfter *uint64) (Signature, error) {
	payload, err := EncodeCancelAction(action)
	if err != nil {
		return Signature{}, err
	}
	hash := actionHash(payload, nonce, vaultAddress, expiresAfter)
	digest, err := typedDataHash(hash, s.isMainnet)
	if err != nil {
		return Signature{}, err
	}
	sig, err := crypto.Sign(digest, s.privKey)
	if err != nil {
		return Signature{}, err
	}
	return signatureFromBytes(sig)
}

func (s *Signer) SignUSDClassTransfer(action *USDClassTransferAction) (Signature, error) {
	if action == nil {
		return Signature{}, errors.New("usd class transfer action is required")
	}
	if action.SignatureChainID == "" {
		action.SignatureChainID = defaultSignatureChainID
	}
	if action.HyperliquidChain == "" {
		action.HyperliquidChain = chainName(s.isMainnet)
	}
	digest, err := userSignedTypedDataHash(*action)
	if err != nil {
		return Signature{}, err
	}
	sig, err := crypto.Sign(digest, s.privKey)
	if err != nil {
		return Signature{}, err
	}
	return signatureFromBytes(sig)
}

func actionHash(action []byte, nonce uint64, vaultAddress *common.Address, expiresAfter *uint64) []byte {
	buf := bytes.NewBuffer(action)
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], nonce)
	buf.Write(nonceBytes[:])
	if vaultAddress == nil {
		buf.WriteByte(0x00)
	} else {
		buf.WriteByte(0x01)
		buf.Write(vaultAddress.Bytes())
	}
	if expiresAfter != nil {
		buf.WriteByte(0x00)
		var expBytes [8]byte
		binary.BigEndian.PutUint64(expBytes[:], *expiresAfter)
		buf.Write(expBytes[:])
	}
	return crypto.Keccak256(buf.Bytes())
}

func typedDataHash(actionHash []byte, isMainnet bool) ([]byte, error) {
	source := "a"
	if !isMainnet {
		source = "b"
	}
	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"Agent": {
				{Name: "source", Type: "string"},
				{Name: "connectionId", Type: "bytes32"},
			},
		},
		PrimaryType: "Agent",
		Domain: apitypes.TypedDataDomain{
			Name:              "Exchange",
			Version:           "1",
			ChainId:           math.NewHexOrDecimal256(1337),
			VerifyingContract: "0x0000000000000000000000000000000000000000",
		},
		Message: apitypes.TypedDataMessage{
			"source":       source,
			"connectionId": hexutil.Encode(actionHash),
		},
	}
	domainHash, err := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
	if err != nil {
		return nil, err
	}
	messageHash, err := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
	if err != nil {
		return nil, err
	}
	return crypto.Keccak256([]byte("\x19\x01"), domainHash, messageHash), nil
}

func userSignedTypedDataHash(action USDClassTransferAction) ([]byte, error) {
	var chainID math.HexOrDecimal256
	if err := chainID.UnmarshalText([]byte(action.SignatureChainID)); err != nil {
		return nil, err
	}
	typedData := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"HyperliquidTransaction:UsdClassTransfer": {
				{Name: "hyperliquidChain", Type: "string"},
				{Name: "amount", Type: "string"},
				{Name: "toPerp", Type: "bool"},
				{Name: "nonce", Type: "uint64"},
			},
		},
		PrimaryType: "HyperliquidTransaction:UsdClassTransfer",
		Domain: apitypes.TypedDataDomain{
			Name:              "HyperliquidSignTransaction",
			Version:           "1",
			ChainId:           &chainID,
			VerifyingContract: "0x0000000000000000000000000000000000000000",
		},
		Message: apitypes.TypedDataMessage{
			"hyperliquidChain": action.HyperliquidChain,
			"amount":           action.Amount,
			"toPerp":           action.ToPerp,
			"nonce":            strconv.FormatUint(action.Nonce, 10),
		},
	}
	domainHash, err := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
	if err != nil {
		return nil, err
	}
	messageHash, err := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
	if err != nil {
		return nil, err
	}
	return crypto.Keccak256([]byte("\x19\x01"), domainHash, messageHash), nil
}

func signatureFromBytes(sig []byte) (Signature, error) {
	if len(sig) != 65 {
		return Signature{}, fmt.Errorf("unexpected signature length %d", len(sig))
	}
	r := hexutil.Encode(sig[:32])
	s := hexutil.Encode(sig[32:64])
	v := int(sig[64]) + 27
	return Signature{R: r, S: s, V: v}, nil
}

const defaultSignatureChainID = "0x66eee"

func chainName(isMainnet bool) string {
	if isMainnet {
		return "Mainnet"
	}
	return "Testnet"
}
