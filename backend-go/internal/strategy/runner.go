// runner.go — 单策略实例：goroutine 管理 + 串行事件队列。
package strategy

import (
	"context"
	"log"
	"time"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

const eventQueueSize = 256

// runner 包装一个 Strategy 实例，在专属 goroutine 内串行处理事件。
type runner struct {
	id       int64
	kind     string
	impl     Strategy
	eventCh  chan *store.OrderRow
	stopCh   chan struct{}
	doneCh   chan struct{}
	st       *store.Store
	emitFn   func(event string, data any)
}

func newRunner(id int64, kind string, impl Strategy, st *store.Store, emitFn func(string, any)) *runner {
	return &runner{
		id:      id,
		kind:    kind,
		impl:    impl,
		eventCh: make(chan *store.OrderRow, eventQueueSize),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		st:      st,
		emitFn:  emitFn,
	}
}

// start 在后台 goroutine 启动事件循环。
func (r *runner) start(ctx context.Context) error {
	if err := r.impl.Start(ctx); err != nil {
		return err
	}
	go r.loop(ctx)
	return nil
}

func (r *runner) loop(ctx context.Context) {
	defer close(r.doneCh)
	reconcile := time.NewTicker(30 * time.Second)
	defer reconcile.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case o := <-r.eventCh:
			if o == nil {
				continue
			}
			if err := r.impl.OnOrderUpdate(ctx, o); err != nil {
				log.Printf("strategy %d OnOrderUpdate order %d: %v", r.id, o.ID, err)
				_ = r.st.UpdateStrategyStatus(ctx, r.id, "error", err.Error())
				r.emitFn("strategy", map[string]any{"id": r.id, "status": "error", "lastError": err.Error()})
			}
		case <-reconcile.C:
			if err := r.impl.Reconcile(ctx); err != nil {
				log.Printf("strategy %d reconcile: %v", r.id, err)
			}
		}
	}
}

// send 投递一个订单事件（非阻塞，满则丢弃）。
func (r *runner) send(o *store.OrderRow) {
	select {
	case r.eventCh <- o:
	default:
	}
}

// stop 信号关闭事件循环并等待退出。
func (r *runner) stop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
	<-r.doneCh
}
