// manager.go — 常驻策略引擎 runtime：生命周期管理 + 事件分发 + 开机恢复。
package strategy

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/jiangbohhh/candleforge/backend-go/internal/broker"
	"github.com/jiangbohhh/candleforge/backend-go/internal/events"
	"github.com/jiangbohhh/candleforge/backend-go/internal/risk"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/strategy/grid"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

// Env 是 Manager 向每个策略实例注入的运行时依赖。
type Env struct {
	Store   *store.Store
	Brokers func(accountID int64) (broker.Broker, error) // BrokerFor resolver
	Risk    *risk.Engine
	PriceFn func(symbol string) float64
	Hub     *ws.Hub
}

// Manager 负责所有策略实例的生命周期。
type Manager struct {
	env     Env
	maxRun  int
	bus     *events.Bus
	unsubFn func() // events.Bus 取消订阅

	mu      sync.RWMutex
	runners map[int64]*runner
}

// NewManager 构建 Manager；不启动任何策略。
func NewManager(env Env, bus *events.Bus, maxRunning int) *Manager {
	m := &Manager{
		env:     env,
		maxRun:  maxRunning,
		bus:     bus,
		runners: make(map[int64]*runner),
	}
	// 全局订阅（strategyID=0）接收所有订单事件，再按 StrategyID 路由
	globalCh := make(chan *store.OrderRow, 512)
	m.unsubFn = bus.Subscribe(0, globalCh)
	go m.dispatchLoop(globalCh)
	return m
}

// dispatchLoop 监听全局事件通道，按 StrategyID 分发给对应 runner。
func (m *Manager) dispatchLoop(ch chan *store.OrderRow) {
	for o := range ch {
		if o == nil || o.StrategyID == 0 {
			continue
		}
		m.mu.RLock()
		r, ok := m.runners[o.StrategyID]
		m.mu.RUnlock()
		if ok {
			r.send(o)
		}
	}
}

// ResumeAll 开机时恢复所有 running 状态的策略（5 步对账）。
func (m *Manager) ResumeAll(ctx context.Context) {
	rows, err := m.env.Store.ListRunningStrategies(ctx)
	if err != nil {
		log.Printf("strategy manager ResumeAll: list: %v", err)
		return
	}
	for _, row := range rows {
		if err := m.resumeOne(ctx, row); err != nil {
			log.Printf("strategy manager ResumeAll: resume %d %q: %v", row.ID, row.Name, err)
			_ = m.env.Store.UpdateStrategyStatus(ctx, row.ID, "error", err.Error())
		}
	}
}

func (m *Manager) resumeOne(ctx context.Context, row store.StrategyRow) error {
	d := Lookup(row.Kind)
	if d == nil || !d.Runnable {
		return fmt.Errorf("unknown or non-runnable strategy kind %q", row.Kind)
	}
	impl, err := m.buildImpl(ctx, row)
	if err != nil {
		return err
	}
	r := newRunner(row.ID, row.Kind, impl, m.env.Store, m.emitStrategyRaw)
	if err := r.impl.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	// 恢复后直接把事件循环跑起来（不再调 Start，因为策略已在 running 状态）
	r.stopCh = make(chan struct{})
	r.doneCh = make(chan struct{})
	go r.loop(ctx)

	m.mu.Lock()
	m.runners[row.ID] = r
	m.mu.Unlock()
	log.Printf("strategy %d %q resumed", row.ID, row.Name)
	return nil
}

// StartStrategy 启动一个已创建的策略实例。
func (m *Manager) StartStrategy(ctx context.Context, strategyID int64) error {
	row, err := m.env.Store.GetStrategy(ctx, strategyID)
	if err != nil {
		return fmt.Errorf("get strategy: %w", err)
	}
	if row.Status == "running" {
		return fmt.Errorf("strategy already running")
	}

	// 并行数量检查
	n, err := m.env.Store.CountRunningStrategies(ctx)
	if err != nil {
		return err
	}
	if n >= m.maxRun {
		return fmt.Errorf("max running strategies (%d) reached", m.maxRun)
	}

	// 子账户占用检查
	if row.AccountID != 0 {
		if err := m.checkAccountFree(ctx, row.AccountID, strategyID); err != nil {
			return err
		}
	}

	d := Lookup(row.Kind)
	if d == nil || !d.Runnable {
		return fmt.Errorf("strategy kind %q is not runnable", row.Kind)
	}

	impl, err := m.buildImpl(ctx, *row)
	if err != nil {
		return err
	}

	r := newRunner(row.ID, row.Kind, impl, m.env.Store, m.emitStrategyRaw)
	if err := r.start(ctx); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	m.mu.Lock()
	m.runners[row.ID] = r
	m.mu.Unlock()

	if err := m.env.Store.UpdateStrategyStatus(ctx, strategyID, "running", ""); err != nil {
		return err
	}
	m.emitStrategy(strategyID, "running", "")
	return nil
}

// StopStrategy 停止一个运行中的策略实例；liquidate=true 则清仓。
func (m *Manager) StopStrategy(ctx context.Context, strategyID int64, liquidate bool) error {
	m.mu.Lock()
	r, ok := m.runners[strategyID]
	if ok {
		delete(m.runners, strategyID)
	}
	m.mu.Unlock()

	if ok {
		// 先通知 impl 执行停止流程（撤单 + 可选清仓）
		if err := r.impl.Stop(ctx, liquidate); err != nil {
			log.Printf("strategy %d stop impl: %v", strategyID, err)
		}
		// 关闭事件循环
		r.stop()
	}

	if err := m.env.Store.UpdateStrategyStatus(ctx, strategyID, "stopped", ""); err != nil {
		return err
	}
	m.emitStrategy(strategyID, "stopped", "")
	return nil
}

// IsRunning 判断策略是否在 runner map 内。
func (m *Manager) IsRunning(strategyID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.runners[strategyID]
	return ok
}

// ── helpers ──

func (m *Manager) checkAccountFree(ctx context.Context, accountID, strategyID int64) error {
	rows, err := m.env.Store.ListStrategies(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.AccountID == accountID && r.Status == "running" && r.ID != strategyID {
			return fmt.Errorf("account %d already used by strategy %d", accountID, r.ID)
		}
	}
	return nil
}

func (m *Manager) buildImpl(ctx context.Context, row store.StrategyRow) (Strategy, error) {
	switch row.Kind {
	case "grid":
		br, err := m.env.Brokers(row.AccountID)
		if err != nil {
			return nil, fmt.Errorf("broker for account %d: %w", row.AccountID, err)
		}
		return grid.NewLiveGrid(ctx, row, br, m.env.Risk, m.env.Store, m.env.PriceFn, m.emitStrategyRaw)
	default:
		return nil, fmt.Errorf("no live implementation for kind %q", row.Kind)
	}
}

func (m *Manager) emitStrategy(strategyID int64, status, lastError string) {
	m.emitStrategyRaw("strategy", map[string]any{
		"id": strategyID, "status": status, "lastError": lastError,
	})
}

func (m *Manager) emitStrategyRaw(event string, data any) {
	if m.env.Hub != nil {
		m.env.Hub.BroadcastEvent(event, data)
	}
}
