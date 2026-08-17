package market

import (
	"fmt"
	"strings"
)

// 统一 symbol 格式：
//   现货      CRYPTO.BTC-USDT
//   USDT-M 永续 CRYPTO.BTC-USDT.PERP
// 交易所原生格式：BTCUSDT（现货走 api.binance.com，永续走 fapi.binance.com，原生符号相同）

// PerpSuffix 是永续合约统一 symbol 的后缀。
const PerpSuffix = ".PERP"

// IsPerp 判断统一 symbol 是否为 USDT-M 永续合约。
func IsPerp(unified string) bool {
	return strings.HasSuffix(unified, PerpSuffix)
}

// ToNative 将统一 symbol 转为 Binance 原生符号。
// CRYPTO.BTC-USDT -> BTCUSDT；CRYPTO.BTC-USDT.PERP -> BTCUSDT
func ToNative(unified string) (string, error) {
	prefix := "CRYPTO."
	if !strings.HasPrefix(unified, prefix) {
		return "", fmt.Errorf("not a crypto symbol: %s", unified)
	}
	pair := strings.TrimPrefix(unified, prefix)     // BTC-USDT[.PERP]
	pair = strings.TrimSuffix(pair, PerpSuffix)      // BTC-USDT
	parts := strings.SplitN(pair, "-", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("bad symbol format: %s", unified)
	}
	return parts[0] + parts[1], nil // BTCUSDT
}

// FromNative 由 base/quote 资产构造现货统一 symbol。
// (BTC, USDT) -> CRYPTO.BTC-USDT
func FromNative(base, quote string) string {
	return fmt.Sprintf("CRYPTO.%s-%s", base, quote)
}

// PerpOf 由现货统一 symbol 构造对应永续 symbol。
// CRYPTO.BTC-USDT -> CRYPTO.BTC-USDT.PERP（已是 PERP 则原样返回）
func PerpOf(spot string) string {
	if IsPerp(spot) {
		return spot
	}
	return spot + PerpSuffix
}
