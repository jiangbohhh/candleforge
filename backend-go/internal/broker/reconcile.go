package broker

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

const orderReconcileInterval = 30 * time.Second

// pollOpenOrders 每 30s 调一次 ReconcileOrders，兜住 UDS 静默断连。
func pollOpenOrders(stop <-chan struct{}, rec func(context.Context) error, label string) {
	t := time.NewTicker(orderReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := rec(ctx); err != nil {
				log.Printf("%s: reconcile orders: %v", label, err)
			}
			cancel()
		}
	}
}

func reconcileAfterConnect(rec func(context.Context) error, label string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rec(ctx); err != nil {
		log.Printf("%s: reconcile after UDS connect: %v", label, err)
	}
}

// venueFillUnchanged 判断 REST 回写是否相对本地无变化，避免 30s 轮询反复广播挤掉策略事件。
func venueFillUnchanged(o *store.OrderRow, status, execQty string) bool {
	if o == nil || o.Status != status {
		return false
	}
	filled, err := strconv.ParseFloat(execQty, 64)
	if err != nil {
		return false
	}
	d := o.FilledQty - filled
	if d < 0 {
		d = -d
	}
	return d < 1e-12
}
