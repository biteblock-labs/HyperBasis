package exchange

type Tif string

const (
	TifAlo Tif = "Alo"
	TifIoc Tif = "Ioc"
	TifGtc Tif = "Gtc"
)

type LimitOrderType struct {
	Tif Tif `json:"tif"`
}

type OrderTypeWire struct {
	Limit *LimitOrderType `json:"limit,omitempty"`
}

type OrderWire struct {
	Asset      int           `json:"a"`
	IsBuy      bool          `json:"b"`
	Price      string        `json:"p"`
	Size       string        `json:"s"`
	ReduceOnly bool          `json:"r"`
	OrderType  OrderTypeWire `json:"t"`
	Cloid      string        `json:"c,omitempty"`
}

type OrderAction struct {
	Type     string      `json:"type"`
	Orders   []OrderWire `json:"orders"`
	Grouping string      `json:"grouping"`
	Builder  any         `json:"builder,omitempty"`
}

type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V int    `json:"v"`
}

type SignedAction struct {
	Action       any       `json:"action"`
	Nonce        uint64    `json:"nonce"`
	Signature    Signature `json:"signature"`
	VaultAddress *string   `json:"vaultAddress"`
	ExpiresAfter *uint64   `json:"expiresAfter"`
}
