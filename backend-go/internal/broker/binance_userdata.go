// Package broker — Binance User Data Stream: 实时同步订单状态与账户余额。
// 单条 WS goroutine + keepalive ticker；断线指数退避重连。
package broker

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
)

type userDataStream struct {
	broker *BinanceBroker
	stopCh chan struct{}
	once   sync.Once
}

func startUserDataStream(b *BinanceBroker) *userDataStream {
	u := &userDataStream{broker: b, stopCh: make(chan struct{})}
	go u.run()
	return u
}

func (u *userDataStream) stop() {
	u.once.Do(func() { close(u.stopCh) })
}

func (u *userDataStream) run() {
	backoff := time.Second
	for {
		select {
		case <-u.stopCh:
			return
		default:
		}

		listenKey, err := u.broker.client.NewStartUserStreamService().Do(context.Background())
		if err != nil {
			log.Printf("binance UDS: get listenKey: %v; retry in %s", err, backoff)
			if !sleepOrStop(u.stopCh, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		log.Printf("binance UDS: connected (%s)", maskKey(listenKey))
		backoff = time.Second

		if err := u.dialAndListen(listenKey); err != nil {
			log.Printf("binance UDS: %v; reconnecting in %s", err, backoff)
			if !sleepOrStop(u.stopCh, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
		}
	}
}

func (u *userDataStream) dialAndListen(listenKey string) error {
	wsURL := u.broker.opts.wsBase() + "/ws/" + listenKey
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctxKA, cancelKA := context.WithCancel(context.Background())
	defer cancelKA()
	go u.keepalive(ctxKA, listenKey)

	// 单独 goroutine 监听 stopCh 来唤醒阻塞中的 ReadMessage。
	stopReader := make(chan struct{})
	defer close(stopReader)
	go func() {
		select {
		case <-u.stopCh:
			_ = conn.Close()
		case <-stopReader:
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		u.dispatch(msg)
	}
}

func (u *userDataStream) keepalive(ctx context.Context, listenKey string) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := u.broker.client.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(context.Background()); err != nil {
				log.Printf("binance UDS keepalive: %v", err)
			}
		}
	}
}

func (u *userDataStream) dispatch(msg []byte) {
	var head struct {
		E string `json:"e"`
	}
	if err := json.Unmarshal(msg, &head); err != nil {
		return
	}
	switch head.E {
	case "executionReport":
		u.handleExecutionReport(msg)
	case "outboundAccountPosition":
		u.handleAccountUpdate(msg)
	}
}

// executionReport 字段：参见 Binance Spot WS docs。
// 主要字段：i=orderId, X=status, x=execType (NEW/TRADE/CANCELED...),
// l=last fill qty, L=last fill price, z=cumulative filled qty, n=commission,
// S=side, s=symbol(native), E=event time.
func (u *userDataStream) handleExecutionReport(msg []byte) {
	var r struct {
		Symbol        string `json:"s"`
		Side          string `json:"S"`
		OrderID       int64  `json:"i"`
		Status        string `json:"X"`
		ExecType      string `json:"x"`
		LastFillQty   string `json:"l"`
		LastFillPrice string `json:"L"`
		CumFilledQty  string `json:"z"`
		Fee           string `json:"n"`
		EventTime     int64  `json:"E"`
	}
	if err := json.Unmarshal(msg, &r); err != nil {
		return
	}

	brokerOID := strconv.FormatInt(r.OrderID, 10)
	ctx := context.Background()
	local, err := u.broker.store.GetOrderByBrokerOrderID(ctx, brokerOID)
	if err != nil {
		// 可能是外部下单（不经过本服务），忽略。
		return
	}

	status := mapBinanceStatusString(r.Status)
	filled, _ := strconv.ParseFloat(r.CumFilledQty, 64)
	if err := u.broker.store.UpdateOrderFill(ctx, local.ID, status, filled); err != nil {
		log.Printf("binance UDS: update order %d: %v", local.ID, err)
	}

	if r.ExecType == "TRADE" {
		lp, _ := strconv.ParseFloat(r.LastFillPrice, 64)
		lq, _ := strconv.ParseFloat(r.LastFillQty, 64)
		fee, _ := strconv.ParseFloat(r.Fee, 64)
		side := "buy"
		if r.Side == "SELL" {
			side = "sell"
		}
		tradedAt := time.UnixMilli(r.EventTime)
		if err := u.broker.store.InsertTradeFull(ctx, local.ID, local.Symbol, side, lp, lq, fee, tradedAt); err != nil {
			log.Printf("binance UDS: insert trade for order %d: %v", local.ID, err)
		}
		u.broker.broadcastEvent("trade", map[string]any{
			"orderId":  local.ID,
			"symbol":   local.Symbol,
			"side":     side,
			"price":    lp,
			"quantity": lq,
			"fee":      fee,
			"tradedAt": tradedAt,
		})
	}
	u.broker.broadcastOrder(local.ID)
}

func (u *userDataStream) handleAccountUpdate(msg []byte) {
	var r struct {
		Balances []struct {
			Asset  string `json:"a"`
			Free   string `json:"f"`
			Locked string `json:"l"`
		} `json:"B"`
	}
	if err := json.Unmarshal(msg, &r); err != nil {
		return
	}
	ctx := context.Background()
	for _, bal := range r.Balances {
		free, _ := strconv.ParseFloat(bal.Free, 64)
		locked, _ := strconv.ParseFloat(bal.Locked, 64)
		total := free + locked
		if bal.Asset == "USDT" {
			_ = u.broker.store.SetAccountCash(ctx, u.broker.accountID, free)
			continue
		}
		sym := market.FromNative(bal.Asset, "USDT")
		_ = u.broker.store.UpsertPositionRaw(ctx, u.broker.accountID, sym, total, 0)
	}
}

func mapBinanceStatusString(s string) string {
	switch s {
	case "NEW", "PARTIALLY_FILLED":
		return "new"
	case "FILLED":
		return "filled"
	case "CANCELED", "PENDING_CANCEL", "EXPIRED":
		return "canceled"
	case "REJECTED":
		return "rejected"
	default:
		return "new"
	}
}

func sleepOrStop(stop chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

func maskKey(k string) string {
	if len(k) <= 8 {
		return "***"
	}
	return k[:6] + "..."
}
