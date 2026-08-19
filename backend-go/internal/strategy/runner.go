// runner.go — 单策略实例：goroutine 管理 + 串行事件队列。
package strategy

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

const eventQueueSize = 256

// runner 包装一个 Strategy 实例，在专属 goroutine 内串行处理事件。
type runner struct {
	id      int64
	kind    string
	impl    Strategy
	eventCh chan *store.OrderRow
	stopCh  chan struct{}
	doneCh  chan struct{}
	st      *store.Store
	emitFn  func(event string, data any)
	// onExit 在事件循环因 panic 异常退出时回调（正常 stop 不触发），
	// 供 Manager 把 runner 从 map 摘除，避免残留导致无法重启。
	onExit func(err error)
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
	if err := r.safeStart(ctx); err != nil {
		return err
	}
	go r.loop(ctx)
	return nil
}

// safeStart 包裹 impl.Start，把 panic 转为 error，避免下单逻辑 panic 崩掉调用方
// （HTTP handler goroutine）进而杀死整个进程。
func (r *runner) safeStart(ctx context.Context) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("strategy %d Start panic: %v\n%s", r.id, rec, debug.Stack())
			err = fmt.Errorf("panic in Start: %v", rec)
		}
	}()
	return r.impl.Start(ctx)
}

// safeStop 包裹 impl.Stop，把 panic 转为 error（同样运行在调用方 goroutine）。
func (r *runner) safeStop(ctx context.Context, liquidate bool) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("strategy %d Stop panic: %v\n%s", r.id, rec, debug.Stack())
			err = fmt.Errorf("panic in Stop: %v", rec)
		}
	}()
	return r.impl.Stop(ctx, liquidate)
}

// safeReconcile 包裹 impl.Reconcile，把 panic 转为 error（供 resume 时在 boot goroutine 同步调用）。
func (r *runner) safeReconcile(ctx context.Context) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("strategy %d Reconcile panic: %v\n%s", r.id, rec, debug.Stack())
			err = fmt.Errorf("panic in Reconcile: %v", rec)
		}
	}()
	return r.impl.Reconcile(ctx)
}

func (r *runner) loop(ctx context.Context) {
	defer close(r.doneCh)
	// 顶层 recover：事件处理中的任何 panic 都不得逃逸到进程级别。
	// 捕获后把策略置 error、通过 onExit 从 Manager 摘除，然后退出循环（fail-safe：
	// 未知状态下停止交易而非继续）。
	defer func() {
		if rec := recover(); rec != nil {
			err := fmt.Errorf("panic: %v", rec)
			log.Printf("strategy %d event loop panic: %v\n%s", r.id, rec, debug.Stack())
			_ = r.st.UpdateStrategyStatus(ctx, r.id, "error", err.Error())
			r.emitFn("strategy", map[string]any{"id": r.id, "status": "error", "lastError": err.Error()})
			if r.onExit != nil {
				r.onExit(err)
			}
		}
	}()

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
