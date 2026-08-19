// Package broker — BinanceFuturesBroker 把统一 Broker 接口接到 Binance USDT-M 永续（fapi）。
// 仅接受 .PERP 后缀的统一 symbol；强制单向持仓模式（one-way）；杠杆由策略层锁定 1x。
// 订单/持仓实时状态由 binance_futures_userdata.go 的合约 User Data Stream 维持。
package broker

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/adshao/go-binance/v2/futures"

	"github.com/jiangbohhh/candleforge/backend-go/internal/events"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

const (
	BinanceFuturesTestnetRESTBase = "https://testnet.binancefuture.com"
	BinanceFuturesTestnetWSBase   = "wss://fstream.binancefuture.com"
	BinanceFuturesMainnetRESTBase = "https://fapi.binance.com"
	BinanceFuturesMainnetWSBase   = "wss://fstream.binance.com"
)

// BinanceFuturesOptions 构造 BinanceFuturesBroker 所需的参数。
// 注意：Binance 合约 testnet（testnet.binancefuture.com）与现货 testnet 是两套账号/密钥。
type BinanceFuturesOptions struct {
	APIKey, APISecret string
	Mainnet           bool
}

func (o BinanceFuturesOptions) restBase() string {
	if o.Mainnet {
		return BinanceFuturesMainnetRESTBase
	}
	return BinanceFuturesTestnetRESTBase
}

func (o BinanceFuturesOptions) wsBase() string {
	if o.Mainnet {
		return BinanceFuturesMainnetWSBase
	}
	return BinanceFuturesTestnetWSBase
}

// EnvLabel 给 UI 用。
func (o BinanceFuturesOptions) EnvLabel() string {
	if o.Mainnet {
		return "mainnet"
	}
	return "testnet"
}

// BinanceFuturesBroker 实现 Broker 接口（USDT-M 永续）。
type BinanceFuturesBroker struct {
	client    *futures.Client
	store     *store.Store
	hub       *ws.Hub
	bus       *events.Bus
	accountID int64
	opts      BinanceFuturesOptions
	uds       *futuresUserDataStream

	levMu  sync.Mutex      // 保护 levSet
	levSet map[string]bool // 已确认设为 1x 杠杆的 native symbol
}

// NewBinanceFuturesBroker 构造 broker：设单向持仓模式 → 同步余额/持仓 → 启动 UDS。
// 显式设 BaseURL，避免依赖 futures.UseTestnet 包级全局（会连带切换行情 WS）。
func NewBinanceFuturesBroker(ctx context.Context, st *store.Store, hub *ws.Hub, bus *events.Bus, accountID int64, opts BinanceFuturesOptions) (*BinanceFuturesBroker, error) {
	c := futures.NewClient(opts.APIKey, opts.APISecret)
	c.BaseURL = opts.restBase()
	b := &BinanceFuturesBroker{client: c, store: st, hub: hub, bus: bus, accountID: accountID, opts: opts, levSet: map[string]bool{}}

	// 强制单向持仓模式；已是单向时交易所返回 -4059，视为成功。
	if err := c.NewChangePositionModeService().DualSide(false).Do(ctx); err != nil &&
		!strings.Contains(err.Error(), "-4059") && !strings.Contains(err.Error(), "No need to change") {
		return nil, fmt.Errorf("set one-way position mode: %w", err)
	}

	if err := b.SyncBalances(ctx); err != nil {
		return nil, fmt.Errorf("initial futures balance sync: %w", err)
	}
	b.uds = startFuturesUserDataStream(b)
	return b, nil
}

// Stop 关闭 UDS 循环。
func (b *BinanceFuturesBroker) Stop() {
	if b.uds != nil {
		b.uds.stop()
	}
}

// SyncBalances 拉一次合约账户：USDT 可用余额 → accounts.cash；
// 非零仓位（签名数量 + 开仓均价）→ positions（交易所即真相）。
func (b *BinanceFuturesBroker) SyncBalances(ctx context.Context) error {
	acct, err := b.client.NewGetAccountService().Do(ctx)
	if err != nil {
		return err
	}
	for _, a := range acct.Assets {
		if a.Asset != "USDT" {
			continue
		}
		free, _ := strconv.ParseFloat(a.AvailableBalance, 64)
		if err := b.store.SetAccountCash(ctx, b.accountID, free); err != nil {
			return err
		}
	}
	for _, p := range acct.Positions {
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if amt == 0 {
			continue
		}
		sym, ok := perpFromNative(p.Symbol)
		if !ok {
			continue
		}
		entry, _ := strconv.ParseFloat(p.EntryPrice, 64)
		if err := b.store.UpsertPositionRaw(ctx, b.accountID, sym, amt, entry); err != nil {
			return err
		}
	}
	return nil
}

// PlaceOrder 把统一请求转成 Binance USDT-M 单。
// 流程：先写本地 order(status=new) → 发 REST → 回写 orderId/status。
func (b *BinanceFuturesBroker) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*store.OrderRow, error) {
	if !market.IsPerp(req.Symbol) {
		return nil, fmt.Errorf("futures broker only accepts .PERP symbols, got %s", req.Symbol)
	}
	native, err := market.ToNative(req.Symbol)
	if err != nil {
		return nil, err
	}

	// C6: 交易前确保该 symbol 在交易所是 1x 杠杆（全系统按 1x 建模，Binance 默认约 20x）。
	// 失败即拒绝下单，绝不在未知杠杆下开仓。
	if err := b.ensureLeverage(ctx, native); err != nil {
		return nil, err
	}

	localID, _, err := b.store.InsertOrder(ctx, store.InsertOrderParams{
		AccountID:  req.AccountID,
		Symbol:     req.Symbol,
		Side:       req.Side,
		OrderType:  req.OrderType,
		Price:      req.Price,
		Quantity:   req.Quantity,
		StrategyID: req.StrategyID,
	})
	if err != nil {
		return nil, err
	}

	// C2: 幂等 clientOrderId，落库后随 REST 提交。
	coid := clientOrderID(localID)
	_ = b.store.SetClientOrderID(ctx, localID, coid)

	svc := b.client.NewCreateOrderService().
		Symbol(native).
		NewClientOrderID(coid).
		Quantity(strconv.FormatFloat(req.Quantity, 'f', -1, 64))

	if req.Side == "sell" {
		svc = svc.Side(futures.SideTypeSell)
	} else {
		svc = svc.Side(futures.SideTypeBuy)
	}
	if req.ReduceOnly {
		svc = svc.ReduceOnly(true)
	}

	if req.OrderType == "limit" {
		svc = svc.Type(futures.OrderTypeLimit).
			TimeInForce(futures.TimeInForceTypeGTC).
			Price(strconv.FormatFloat(req.Price, 'f', -1, 64))
	} else {
		svc = svc.Type(futures.OrderTypeMarket)
	}

	resp, err := svc.Do(ctx)
	if err != nil {
		// M15: 超时/网络错误 ≠ 拒单。先按 clientOrderId 查是否已受理，已受理则收养。
		if got, qerr := b.client.NewGetOrderService().Symbol(native).OrigClientOrderID(coid).Do(ctx); qerr == nil && got != nil {
			b.adoptOrder(ctx, localID, got.OrderID, mapBinanceStatusString(string(got.Status)), got.ExecutedQuantity)
			return b.store.GetOrder(ctx, localID)
		}
		_ = b.store.UpdateOrderStatus(ctx, localID, "rejected")
		b.broadcastOrder(localID)
		return nil, err
	}

	b.adoptOrder(ctx, localID, resp.OrderID, mapBinanceStatusString(string(resp.Status)), resp.ExecutedQuantity)
	return b.store.GetOrder(ctx, localID)
}

// ensureLeverage 把指定 symbol 的杠杆设为 1x 并回读校验（每 symbol 一次，结果缓存）。
// 交易所返回的实际杠杆必须为 1，否则报错——避免在默认高杠杆下开仓。
func (b *BinanceFuturesBroker) ensureLeverage(ctx context.Context, native string) error {
	b.levMu.Lock()
	defer b.levMu.Unlock()
	if b.levSet[native] {
		return nil
	}
	res, err := b.client.NewChangeLeverageService().Symbol(native).Leverage(1).Do(ctx)
	if err != nil {
		return fmt.Errorf("set 1x leverage for %s: %w", native, err)
	}
	if res == nil || res.Leverage != 1 {
		return fmt.Errorf("leverage for %s not 1x after set (got %v)", native, res)
	}
	b.levSet[native] = true
	log.Printf("binance futures: %s leverage set to 1x", native)
	return nil
}

// adoptOrder 把交易所订单信息回写到本地行并广播。
func (b *BinanceFuturesBroker) adoptOrder(ctx context.Context, localID, brokerOrderID int64, status, execQty string) {
	_ = b.store.SetOrderBrokerOrderID(ctx, localID, strconv.FormatInt(brokerOrderID, 10))
	filled, _ := strconv.ParseFloat(execQty, 64)
	_ = b.store.UpdateOrderFill(ctx, localID, status, filled)
	b.broadcastOrder(localID)
}

// CancelOrder 走 fapi 撤单；本地状态由 UDS 兜底，但请求成功即写一次。
func (b *BinanceFuturesBroker) CancelOrder(ctx context.Context, accountID, orderID int64) error {
	o, err := b.store.GetOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if o.AccountID != accountID {
		return fmt.Errorf("order %d does not belong to account %d", orderID, accountID)
	}
	if o.BrokerOrderID == "" {
		return fmt.Errorf("local order %d has no broker_order_id", orderID)
	}
	native, err := market.ToNative(o.Symbol)
	if err != nil {
		return err
	}
	brokerOID, err := strconv.ParseInt(o.BrokerOrderID, 10, 64)
	if err != nil {
		return fmt.Errorf("bad broker_order_id %q: %w", o.BrokerOrderID, err)
	}
	if _, err := b.client.NewCancelOrderService().Symbol(native).OrderID(brokerOID).Do(ctx); err != nil {
		return err
	}
	_ = b.store.UpdateOrderStatus(ctx, orderID, "canceled")
	b.broadcastOrder(orderID)
	return nil
}

// ListOrders / GetPositions 直接读本地——UDS 保证两者实时一致。
func (b *BinanceFuturesBroker) ListOrders(ctx context.Context, accountID int64) ([]store.OrderRow, error) {
	return b.store.ListOrders(ctx, accountID, 100)
}

func (b *BinanceFuturesBroker) GetPositions(ctx context.Context, accountID int64) ([]store.PositionView, error) {
	return b.store.ListPositions(ctx, accountID)
}

// ── 内部 helper ──

func (b *BinanceFuturesBroker) broadcastOrder(localID int64) {
	o, err := b.store.GetOrder(context.Background(), localID)
	if err != nil {
		log.Printf("binance futures: broadcastOrder load %d: %v", localID, err)
		return
	}
	if b.hub != nil {
		b.hub.BroadcastEvent("order", o)
	}
	if b.bus != nil {
		b.bus.PublishOrder(o)
	}
}

// perpFromNative 把 USDT-M 原生符号转回统一 PERP symbol：BTCUSDT → CRYPTO.BTC-USDT.PERP。
func perpFromNative(native string) (string, bool) {
	base, ok := strings.CutSuffix(native, "USDT")
	if !ok || base == "" {
		return "", false
	}
	return market.PerpOf(market.FromNative(base, "USDT")), true
}
