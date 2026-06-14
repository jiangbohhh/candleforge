// Package broker — BinanceBroker 把 SimBroker 用过的统一接口接到 Binance 现货 REST。
// 订单/持仓的实时状态由 binance_userdata.go 中的 User Data Stream 维持。
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	binance "github.com/adshao/go-binance/v2"

	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

const (
	BinanceTestnetRESTBase = "https://testnet.binance.vision"
	BinanceTestnetWSBase   = "wss://testnet.binance.vision"
	BinanceMainnetRESTBase = "https://api.binance.com"
	BinanceMainnetWSBase   = "wss://stream.binance.com:9443"
)

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
	accountID int64
	opts      BinanceOptions
	uds       *userDataStream
}

// NewBinanceBroker 构造 broker，同步初始余额，启动 User Data Stream。
// ctx 仅用于初始余额同步；失败即报错，避免上线后悄悄空跑。
func NewBinanceBroker(ctx context.Context, st *store.Store, hub *ws.Hub, accountID int64, opts BinanceOptions) (*BinanceBroker, error) {
	c := binance.NewClient(opts.APIKey, opts.APISecret)
	c.BaseURL = opts.restBase()
	b := &BinanceBroker{client: c, store: st, hub: hub, accountID: accountID, opts: opts}

	if err := b.SyncBalances(ctx); err != nil {
		return nil, fmt.Errorf("initial balance sync: %w", err)
	}
	b.uds = startUserDataStream(b)
	return b, nil
}

// Stop 关闭 UDS 循环。
func (b *BinanceBroker) Stop() {
	if b.uds != nil {
		b.uds.stop()
	}
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
		if err := b.store.UpsertPositionRaw(ctx, b.accountID, sym, total, 0); err != nil {
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
		AccountID: req.AccountID,
		Symbol:    req.Symbol,
		Side:      req.Side,
		OrderType: req.OrderType,
		Price:     req.Price,
		Quantity:  req.Quantity,
	})
	if err != nil {
		return nil, err
	}

	svc := b.client.NewCreateOrderService().
		Symbol(native).
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
		_ = b.store.UpdateOrderStatus(ctx, localID, "rejected")
		b.broadcastOrder(localID)
		return nil, err
	}

	brokerOID := strconv.FormatInt(resp.OrderID, 10)
	_ = b.store.SetOrderBrokerOrderID(ctx, localID, brokerOID)
	status := mapBinanceStatus(resp.Status)
	filled, _ := strconv.ParseFloat(resp.ExecutedQuantity, 64)
	_ = b.store.UpdateOrderFill(ctx, localID, status, filled)
	b.broadcastOrder(localID)

	return b.store.GetOrder(ctx, localID)
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

// ── 内部 helper ──

func (b *BinanceBroker) broadcastOrder(localID int64) {
	if b.hub == nil {
		return
	}
	o, err := b.store.GetOrder(context.Background(), localID)
	if err != nil {
		log.Printf("binance: broadcastOrder load %d: %v", localID, err)
		return
	}
	b.broadcastEvent("order", o)
}

func (b *BinanceBroker) broadcastEvent(t string, data any) {
	if b.hub == nil {
		return
	}
	msg, err := json.Marshal(map[string]any{"type": t, "data": data})
	if err != nil {
		return
	}
	b.hub.Broadcast(msg)
}

func mapBinanceStatus(s binance.OrderStatusType) string {
	switch s {
	case binance.OrderStatusTypeNew, binance.OrderStatusTypePartiallyFilled:
		return "new"
	case binance.OrderStatusTypeFilled:
		return "filled"
	case binance.OrderStatusTypeCanceled, binance.OrderStatusTypePendingCancel, binance.OrderStatusTypeExpired:
		return "canceled"
	case binance.OrderStatusTypeRejected:
		return "rejected"
	default:
		return "new"
	}
}
