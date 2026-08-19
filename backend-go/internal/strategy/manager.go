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

	// startMu 串行化 start/stop 的「校验 + 占用检查 + 认领」临界区，
	// 关闭 check-then-start 竞态（同策略双跑 / 两策略抢同一子账户）。
	// 慢操作（下单）在锁外执行，权威并发闸门是 DB 的条件状态迁移。
	startMu sync.Mutex
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
	r.onExit = m.runnerExitHandler(row.ID)

	// H9: 撤销孤儿订单——有 strategy_id、status=new、却无 active intent 对应的挂单，
	// 是崩溃在下单三步之间残留的无跟踪单。恢复时清掉，避免其成交后库存永久漂移。
	if br, berr := m.env.Brokers(row.AccountID); berr == nil {
		if orphans, oerr := m.env.Store.ListOrphanStrategyOrders(ctx, row.ID); oerr == nil {
			for _, o := range orphans {
				if err := br.CancelOrder(ctx, row.AccountID, o.ID); err != nil {
					log.Printf("strategy %d: cancel orphan order %d: %v", row.ID, o.ID, err)
				} else {
					log.Printf("strategy %d: canceled orphan order %d", row.ID, o.ID)
				}
			}
		}
	}

	if err := r.safeReconcile(ctx); err != nil {
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

// runnerExitHandler 返回一个 onExit 回调：当某策略的事件循环因 panic 异常退出时，
// 把它从 runners map 摘除，使其可被重新启动（状态已在 loop 内置为 error）。
func (m *Manager) runnerExitHandler(id int64) func(error) {
	return func(err error) {
		m.mu.Lock()
		delete(m.runners, id)
		m.mu.Unlock()
		log.Printf("strategy %d runner removed after abnormal exit: %v", id, err)
	}
}

// StartStrategy 启动一个已创建的策略实例。
//
// 并发安全：校验 + 占用检查 + 认领在 startMu 下串行完成，认领用条件 SQL
// （TryMarkStrategyRunning）作为权威闸门；随后释放锁再执行慢操作（impl.Start 下单）。
// 这样既拦住同策略双启动，也拦住两个策略抢同一子账户，且不在下单期间长时间持锁。
func (m *Manager) StartStrategy(ctx context.Context, strategyID int64) error {
	r, err := m.claimAndBuild(ctx, strategyID)
	if err != nil {
		return err
	}

	// 慢操作在锁外：impl.Start 会向交易所下初始单/网格单。
	// 认领已在 DB 落地（status=running），并发启动此时已被拦截。
	if err := r.start(ctx); err != nil {
		// 回滚认领：置 error，让策略可被重新启动。
		_ = m.env.Store.UpdateStrategyStatus(ctx, strategyID, "error", err.Error())
		m.emitStrategy(strategyID, "error", err.Error())
		return fmt.Errorf("start: %w", err)
	}

	m.mu.Lock()
	m.runners[strategyID] = r
	m.mu.Unlock()

	m.emitStrategy(strategyID, "running", "")
	return nil
}

// claimAndBuild 在 startMu 下完成校验、占用检查、状态认领与 impl 构建。
// 成功返回未启动的 runner；调用方在锁外调用 r.start 完成下单。
func (m *Manager) claimAndBuild(ctx context.Context, strategyID int64) (*runner, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	if m.IsRunning(strategyID) {
		return nil, fmt.Errorf("strategy already running")
	}

	row, err := m.env.Store.GetStrategy(ctx, strategyID)
	if err != nil {
		return nil, fmt.Errorf("get strategy: %w", err)
	}
	if row.Status == "running" {
		return nil, fmt.Errorf("strategy already running")
	}

	// 并行数量检查
	n, err := m.env.Store.CountRunningStrategies(ctx)
	if err != nil {
		return nil, err
	}
	if n >= m.maxRun {
		return nil, fmt.Errorf("max running strategies (%d) reached", m.maxRun)
	}

	// 子账户占用检查
	if row.AccountID != 0 {
		if err := m.checkAccountFree(ctx, row.AccountID, strategyID); err != nil {
			return nil, err
		}
	}

	d := Lookup(row.Kind)
	if d == nil || !d.Runnable {
		return nil, fmt.Errorf("strategy kind %q is not runnable", row.Kind)
	}

	impl, err := m.buildImpl(ctx, *row)
	if err != nil {
		return nil, err
	}

	// 权威认领：条件迁移到 running。并发调用中只有一个能拿到 true。
	claimed, err := m.env.Store.TryMarkStrategyRunning(ctx, strategyID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, fmt.Errorf("strategy already running")
	}

	r := newRunner(row.ID, row.Kind, impl, m.env.Store, m.emitStrategyRaw)
	r.onExit = m.runnerExitHandler(row.ID)
	return r, nil
}

// StopStrategy 停止一个运行中的策略实例；liquidate=true 则清仓。
func (m *Manager) StopStrategy(ctx context.Context, strategyID int64, liquidate bool) error {
	// 在 startMu 下摘除 runner 并把状态落为 stopped，使并发的 StartStrategy
	// 要么在本次停止前完整跑完，要么看到 stopped/无 runner，不会与停止交错。
	m.startMu.Lock()
	m.mu.Lock()
	r, ok := m.runners[strategyID]
	if ok {
		delete(m.runners, strategyID)
	}
	m.mu.Unlock()
	err := m.env.Store.UpdateStrategyStatus(ctx, strategyID, "stopped", "")
	m.startMu.Unlock()
	if err != nil {
		return err
	}

	if ok {
		// 先通知 impl 执行停止流程（撤单 + 可选清仓），panic 不得崩进程。
		if err := r.safeStop(ctx, liquidate); err != nil {
			log.Printf("strategy %d stop impl: %v", strategyID, err)
		}
		// 关闭事件循环
		r.stop()
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
