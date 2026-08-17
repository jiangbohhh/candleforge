package market

import "testing"

func TestToNative(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"CRYPTO.BTC-USDT", "BTCUSDT", false},
		{"CRYPTO.BTC-USDT.PERP", "BTCUSDT", false},
		{"CRYPTO.ETH-USDT.PERP", "ETHUSDT", false},
		{"BTCUSDT", "", true},
		{"CRYPTO.BTCUSDT", "", true},
	}
	for _, c := range cases {
		got, err := ToNative(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ToNative(%q): want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ToNative(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestIsPerpAndPerpOf(t *testing.T) {
	if IsPerp("CRYPTO.BTC-USDT") {
		t.Error("spot symbol misdetected as perp")
	}
	if !IsPerp("CRYPTO.BTC-USDT.PERP") {
		t.Error("perp symbol not detected")
	}
	if got := PerpOf("CRYPTO.BTC-USDT"); got != "CRYPTO.BTC-USDT.PERP" {
		t.Errorf("PerpOf = %q", got)
	}
	if got := PerpOf("CRYPTO.BTC-USDT.PERP"); got != "CRYPTO.BTC-USDT.PERP" {
		t.Errorf("PerpOf idempotency broken: %q", got)
	}
}
