// Package broker — BinanceBroker 把 SimBroker 用过的统一接口接到 Binance 现货 REST。
// 订单/持仓的实时状态由 binance_userdata.go 中的 User Data Stream 维持。
package broker

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"

	binance "github.com/adshao/go-binance/v2"

	"github.com/jiangbohhh/candleforge/backend-go/internal/events"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

const (
	BinanceTestnetRESTBase = "https://testnet.binance.vision"
	BinanceTestnetWSBase   = "wss://ws-api.testnet.binance.vision/ws-api/v3"
	BinanceMainnetRESTBase = "https://api.binance.com"
	BinanceMainnetWSBase   = "wss://ws-api.binance.com/ws-api/v3"
)

// clientOrderID 由本地订单 ID 生成幂等的交易所 clientOrderId（≤36 字符，
// 字母数字，Binance 现货/合约通用）。同一本地订单重试用同一 ID，可在 REST
// 超时后按其查单，避免重复下单。
func clientOrderID(localID int64) string {
	return fmt.Sprintf("cf%d", localID)
}

// BinanceOptions 构造 BinanceBroker 所需的参数。
type BinanceOptions struct {
	APIKey, APISecret string
	Mainnet           bool
	RESTBaseOverride  string // 空 → 走 Mainnet/Testnet 默认
	WSBaseOverride    string // 空 → 走 Mainnet/Testnet 默认
}

func (o BinanceOptions) restBase() string {
	if o.RESTBaseOverride != "" {
		return o.RESTBaseOverride
	}
	if o.Mainnet {
		return BinanceMainnetRESTBase
	}
	return BinanceTestnetRESTBase
}

func (o BinanceOptions) wsBase() string {
	if o.WSBaseOverride != "" {
		return o.WSBaseOverride
	}
	if o.Mainnet {
		return BinanceMainnetWSBase
	}
	return BinanceTestnetWSBase
}

// EnvLabel 给 UI 用，方便区分实盘 / 测试网。
func (o BinanceOptions) EnvLabel() string {
	if o.Mainnet {
		return "mainnet"
	}
	return "testnet"
}

// BinanceBroker 实现 Broker 接口；线程安全靠 *binance.Client 自身保证。
type BinanceBroker struct {
	client    *binance.Client
	store     *store.Store
	hub       *ws.Hub
	bus       *events.Bus
	accountID int64
	opts      BinanceOptions
	uds       *userDataStream

	reconcileMu sync.Mutex
	pollStop    chan struct{}
	pollOnce    sync.Once
}

// NewBinanceBroker 构造 broker，同步初始余额，启动 User Data Stream。
// ctx 仅用于初始余额同步；失败即报错，避免上线后悄悄空跑。
func NewBinanceBroker(ctx context.Context, st *store.Store, hub *ws.Hub, bus *events.Bus, accountID int64, opts BinanceOptions) (*BinanceBroker, error) {
	c := binance.NewClient(opts.APIKey, opts.APISecret)
	c.BaseURL = opts.restBase()
	b := &BinanceBroker{client: c, store: st, hub: hub, bus: bus, accountID: accountID, opts: opts}

	if err := b.SyncBalances(ctx); err != nil {
		return nil, fmt.Errorf("initial balance sync: %w", err)
	}
	b.pollStop = make(chan struct{})
	b.uds = startUserDataStream(b)
	go pollOpenOrders(b.pollStop, b.ReconcileOrders, "binance")
	return b, nil
}

// Stop 关闭 UDS 循环与订单对账轮询。
func (b *BinanceBroker) Stop() {
	if b.uds != nil {
		b.uds.stop()
	}
	b.pollOnce.Do(func() {
		if b.pollStop != nil {
			close(b.pollStop)
		}
	})
}

// SyncBalances 拉一次 Binance 账户余额覆盖本地 cash + positions。
// USDT free → accounts.cash；其他非零 free+locked → positions（avg_price=0，UI 标注未知）。
func (b *BinanceBroker) SyncBalances(ctx context.Context) error {
	acct, err := b.client.NewGetAccountService().Do(ctx)
	if err != nil {
		return err
	}
	for _, bal := range acct.Balances {
		free, _ := strconv.ParseFloat(bal.Free, 64)
		locked, _ := strconv.ParseFloat(bal.Locked, 64)
		total := free + locked
		if bal.Asset == "USDT" {
			if err := b.store.SetAccountCash(ctx, b.accountID, free); err != nil {
				return err
			}
			continue
		}
		if total <= 0 {
			continue
		}
		// 假设所有非 USDT 持仓的 quote 都是 USDT；如未来需扩展，可换成 SymbolByBase 查询。
		sym := market.FromNative(bal.Asset, "USDT")
		// C4: 现货接口不返回成本价，从本地成交流水复算加权均价。
		if err := b.store.UpsertSpotPositionWithCost(ctx, b.accountID, sym, total); err != nil {
			return err
		}
	}
	return nil
}

// PlaceOrder 把统一请求转成 Binance 现货单。
// 流程：先写本地 order(status=new) → 发 REST → 用返回的 orderId/status 回写。
func (b *BinanceBroker) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*store.OrderRow, error) {
	native, err := market.ToNative(req.Symbol)
	if err != nil {
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

	// C2: 生成与本地订单绑定的幂等 clientOrderId，落库后随 REST 提交。
	coid := clientOrderID(localID)
	_ = b.store.SetClientOrderID(ctx, localID, coid)

	svc := b.client.NewCreateOrderService().
		Symbol(native).
		NewClientOrderID(coid).
		Quantity(strconv.FormatFloat(req.Quantity, 'f', -1, 64))

	if req.Side == "sell" {
		svc = svc.Side(binance.SideTypeSell)
	} else {
		svc = svc.Side(binance.SideTypeBuy)
	}

	if req.OrderType == "limit" {
		svc = svc.Type(binance.OrderTypeLimit).
			TimeInForce(binance.TimeInForceTypeGTC).
			Price(strconv.FormatFloat(req.Price, 'f', -1, 64))
	} else {
		svc = svc.Type(binance.OrderTypeMarket)
	}

	resp, err := svc.Do(ctx)
	if err != nil {
		// M15: 超时/网络错误 ≠ 拒单。先按 clientOrderId 查交易所是否已受理，
		// 已受理则收养，确认未受理才标 rejected，避免「可能已成交的订单被标
		// rejected 后无人跟踪」以及重试造成的双倍仓位。
		if got, qerr := b.client.NewGetOrderService().Symbol(native).OrigClientOrderID(coid).Do(ctx); qerr == nil && got != nil {
			b.adoptOrder(ctx, localID, got.OrderID, mapBinanceStatus(got.Status), got.ExecutedQuantity)
			return b.store.GetOrder(ctx, localID)
		}
		_ = b.store.UpdateOrderStatus(ctx, localID, "rejected")
		b.broadcastOrder(localID)
		return nil, err
	}

	b.adoptOrder(ctx, localID, resp.OrderID, mapBinanceStatus(resp.Status), resp.ExecutedQuantity)
	return b.store.GetOrder(ctx, localID)
}

// adoptOrder 把交易所订单信息回写到本地行并广播。
func (b *BinanceBroker) adoptOrder(ctx context.Context, localID, brokerOrderID int64, status, execQty string) {
	_ = b.store.SetOrderBrokerOrderID(ctx, localID, strconv.FormatInt(brokerOrderID, 10))
	filled, _ := strconv.ParseFloat(execQty, 64)
	_ = b.store.UpdateOrderFill(ctx, localID, status, filled)
	b.broadcastOrder(localID)
}

// CancelOrder 走 Binance REST 撤单；本地状态由 UDS 兜底，但请求成功即写一次。
func (b *BinanceBroker) CancelOrder(ctx context.Context, accountID, orderID int64) error {
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

// ListOrders / GetPositions 直接读本地——UDS 已经保证两者实时一致。
func (b *BinanceBroker) ListOrders(ctx context.Context, accountID int64) ([]store.OrderRow, error) {
	return b.store.ListOrders(ctx, accountID, 100)
}

func (b *BinanceBroker) GetPositions(ctx context.Context, accountID int64) ([]store.PositionView, error) {
	return b.store.ListPositions(ctx, accountID)
}

// ReconcileOrders 用 REST 对齐本地未终态订单：仍在交易所则回写成交量，
// 不在挂单簿则按 clientOrderId 查终态收养（停机窗口 / UDS 丢包）。
func (b *BinanceBroker) ReconcileOrders(ctx context.Context) error {
	b.reconcileMu.Lock()
	defer b.reconcileMu.Unlock()

	local, err := b.store.ListOpenOrders(ctx, b.accountID)
	if err != nil {
		return err
	}
	if len(local) == 0 {
		return nil
	}

	open, err := b.client.NewListOpenOrdersService().Do(ctx)
	if err != nil {
		return fmt.Errorf("list open orders: %w", err)
	}
	byCoid := make(map[string]*binance.Order, len(open))
	for _, eo := range open {
		if eo != nil && eo.ClientOrderID != "" {
			byCoid[eo.ClientOrderID] = eo
		}
	}

	for i := range local {
		o := &local[i]
		if o.ClientOrderID == "" {
			continue
		}
		if eo, ok := byCoid[o.ClientOrderID]; ok {
			st := mapBinanceStatus(eo.Status)
			if !venueFillUnchanged(o, st, eo.ExecutedQuantity) {
				b.adoptOrder(ctx, o.ID, eo.OrderID, st, eo.ExecutedQuantity)
			}
			continue
		}
		native, nerr := market.ToNative(o.Symbol)
		if nerr != nil {
			continue
		}
		got, qerr := b.client.NewGetOrderService().Symbol(native).OrigClientOrderID(o.ClientOrderID).Do(ctx)
		if qerr != nil || got == nil {
			log.Printf("binance: reconcile order %d coid=%s query: %v", o.ID, o.ClientOrderID, qerr)
			continue
		}
		b.adoptOrder(ctx, o.ID, got.OrderID, mapBinanceStatus(got.Status), got.ExecutedQuantity)
		log.Printf("binance: adopted order %d status=%s filled=%s (missed UDS)", o.ID, got.Status, got.ExecutedQuantity)
	}
	return nil
}

// ── 内部 helper ──

func (b *BinanceBroker) broadcastOrder(localID int64) {
	o, err := b.store.GetOrder(context.Background(), localID)
	if err != nil {
		log.Printf("binance: broadcastOrder load %d: %v", localID, err)
		return
	}
	if b.hub != nil {
		b.hub.BroadcastEvent("order", o)
	}
	if b.bus != nil {
		b.bus.PublishOrder(o)
	}
}

func mapBinanceStatus(s binance.OrderStatusType) string {
	return mapBinanceStatusString(string(s))
}
