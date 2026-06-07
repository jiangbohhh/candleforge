package market

import (
	"fmt"
	"strings"
)

// 统一 symbol 格式：CRYPTO.BTC-USDT
// 交易所原生格式：BTCUSDT

// ToNative 将统一 symbol 转为 Binance 原生符号。
// CRYPTO.BTC-USDT -> BTCUSDT
func ToNative(unified string) (string, error) {
	prefix := "CRYPTO."
	if !strings.HasPrefix(unified, prefix) {
		return "", fmt.Errorf("not a crypto symbol: %s", unified)
	}
	pair := strings.TrimPrefix(unified, prefix) // BTC-USDT
	parts := strings.SplitN(pair, "-", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("bad symbol format: %s", unified)
	}
	return parts[0] + parts[1], nil // BTCUSDT
}

// FromNative 由 base/quote 资产构造统一 symbol。
// (BTC, USDT) -> CRYPTO.BTC-USDT
func FromNative(base, quote string) string {
	return fmt.Sprintf("CRYPTO.%s-%s", base, quote)
}
