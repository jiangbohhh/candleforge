package grid

// fillDecision 是网格对一笔订单状态更新的反应。
// 引擎 OnFill 按整格翻转（±QtyPerGrid），不能在限价单仍挂着时按增量调用，
// 否则会提前翻层并重复补单。部分成交只记账；完全成交才 OnFill；
// 部成后撤单则重挂剩余量，等剩余量成交后再 OnFill（累计成交 = 整格）。
type fillDecision struct {
	applyEngine   bool    // 调用 OnFill（整格步进）
	replaceQty    float64 // 同层重挂该数量；0 = 不重挂
	newConsumed   float64
	markProcessed bool
	trackOnly     bool // 只推进 consumed_qty，intent 仍活动
}

const fillDustFrac = 0.01

func decideGridFill(status string, orderQty, filledQty, consumedQty, qtyPerGrid float64) fillDecision {
	if filledQty < consumedQty {
		filledQty = consumedQty
	}
	if orderQty <= 0 {
		orderQty = qtyPerGrid
	}
	remaining := orderQty - filledQty
	if remaining < 0 {
		remaining = 0
	}
	dust := remaining <= 1e-12 || (qtyPerGrid > 0 && remaining <= qtyPerGrid*fillDustFrac)

	switch status {
	case "filled":
		return fillDecision{applyEngine: true, newConsumed: filledQty, markProcessed: true}
	case "partially_filled":
		if filledQty <= consumedQty+1e-12 {
			return fillDecision{}
		}
		return fillDecision{trackOnly: true, newConsumed: filledQty}
	case "canceled", "rejected":
		if filledQty <= 1e-12 {
			return fillDecision{markProcessed: true, newConsumed: consumedQty, replaceQty: orderQty}
		}
		if dust {
			return fillDecision{applyEngine: true, newConsumed: filledQty, markProcessed: true}
		}
		return fillDecision{markProcessed: true, newConsumed: filledQty, replaceQty: remaining}
	default:
		return fillDecision{}
	}
}
