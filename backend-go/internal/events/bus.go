// Package events 提供进程内订单事件总线，供 broker → 策略引擎单向推送。
package events

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
)

const defaultBufSize = 512

// Bus 是一个轻量的 pub/sub 总线：broker 调用 PublishOrder 推送成交事件，
// 策略引擎通过 Subscribe 注册监听器（按 strategy_id 过滤）。
type Bus struct {
	mu          sync.RWMutex
	subscribers map[int64][]chan *store.OrderRow // key = strategyID; 0 = 全局
	dropped     atomic.Int64                     // 缓冲满被丢弃的事件数（F4 可观测性）
}

func NewBus() *Bus {
	return &Bus{subscribers: make(map[int64][]chan *store.OrderRow)}
}

// Subscribe 注册一个 channel 接收指定策略的订单事件；strategyID=0 接收全部。
// 返回 unsubscribe 函数，调用后清理注册。
func (b *Bus) Subscribe(strategyID int64, ch chan *store.OrderRow) func() {
	b.mu.Lock()
	b.subscribers[strategyID] = append(b.subscribers[strategyID], ch)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		list := b.subscribers[strategyID]
		for i, c := range list {
			if c == ch {
				b.subscribers[strategyID] = append(list[:i], list[i+1:]...)
				break
			}
		}
	}
}

// PublishOrder 将 order 投递给所有匹配的订阅者（非阻塞，满则丢弃）。
func (b *Bus) PublishOrder(o *store.OrderRow) {
	if o == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	targets := make([]chan *store.OrderRow, 0, 4)
	// 全局订阅（strategyID=0）
	targets = append(targets, b.subscribers[0]...)
	// 策略专属订阅
	if o.StrategyID != 0 {
		targets = append(targets, b.subscribers[o.StrategyID]...)
	}
	for _, ch := range targets {
		select {
		case ch <- o:
		default:
			// F4：满丢弃时计数并打日志，把「成交事件静默丢失」变成可观测。
			// 丢失的成交由策略 Reconcile（ListUnprocessedIntents）兜底收养，不会永久漂移。
			n := b.dropped.Add(1)
			if n%100 == 1 {
				log.Printf("events bus: dropped %d order events (subscriber buffer full)", n)
			}
		}
	}
}
