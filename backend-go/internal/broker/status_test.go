package broker

import (
	"testing"

	binance "github.com/adshao/go-binance/v2"
)

func TestMapBinanceStatusPartialFill(t *testing.T) {
	if got := mapBinanceStatus(binance.OrderStatusTypePartiallyFilled); got != "partially_filled" {
		t.Fatalf("spot PARTIALLY_FILLED → %q", got)
	}
	if got := mapBinanceStatusString("PARTIALLY_FILLED"); got != "partially_filled" {
		t.Fatalf("string PARTIALLY_FILLED → %q", got)
	}
	if got := mapBinanceStatusString("NEW"); got != "new" {
		t.Fatalf("NEW → %q", got)
	}
	if got := mapBinanceStatus(binance.OrderStatusTypeFilled); got != "filled" {
		t.Fatalf("FILLED → %q", got)
	}
}

func TestMapBinanceStatusTerminal(t *testing.T) {
	cases := map[string]string{
		"FILLED":          "filled",
		"CANCELED":        "canceled",
		"EXPIRED":         "canceled",
		"PENDING_CANCEL":  "canceled",
		"REJECTED":        "rejected",
		"NEW":             "new",
		"PARTIALLY_FILLED": "partially_filled",
	}
	for in, want := range cases {
		if got := mapBinanceStatusString(in); got != want {
			t.Errorf("%s → %q want %q", in, got, want)
		}
	}
}
