// Package broker — Binance USDT-M 合约 User Data Stream。
//
// 合约仍使用经典 listenKey 体系：
//   POST /fapi/v1/listenKey → wss://fstream.binance{,future}.com/ws/<listenKey>
//   每 ~30min PUT keepalive；listenKeyExpired 事件或断线 → 重新获取并重连。
//
// 手写 gorilla WS 而非 futures.WsUserDataServe：后者的 endpoint 依赖
// futures.UseTestnet 包级全局，会连带切换行情 WS，这里必须按 broker 实例隔离。
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type futuresUserDataStream struct {
	broker *BinanceFuturesBroker
	stopCh chan struct{}
	once   sync.Once
}

func startFuturesUserDataStream(b *BinanceFuturesBroker) *futuresUserDataStream {
	u := &futuresUserDataStream{broker: b, stopCh: make(chan struct{})}
	go u.run()
	return u
}

func (u *futuresUserDataStream) stop() {
	u.once.Do(func() { close(u.stopCh) })
}

func (u *futuresUserDataStream) run() {
	backoff := time.Second
	for {
		select {
		case <-u.stopCh:
			return
		default:
		}

		if err := u.dialAndListen(); err != nil {
			log.Printf("binance futures UDS: %v; reconnect in %s", err, backoff)
			if !sleepOrStop(u.stopCh, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
	}
}

func (u *futuresUserDataStream) dialAndListen() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	listenKey, err := u.broker.client.NewStartUserStreamService().Do(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("start user stream: %w", err)
	}

	wsURL := fmt.Sprintf("%s/ws/%s", u.broker.opts.wsBase(), listenKey)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer conn.Close()

	// stopCh 触发即关连接，让 ReadMessage 出错返回；同 goroutine 顺带做 keepalive。
	stopReader := make(chan struct{})
	defer close(stopReader)
	go func() {
		ka := time.NewTicker(25 * time.Minute)
		defer ka.Stop()
		for {
			select {
			case <-u.stopCh:
				_ = conn.Close()
				return
			case <-stopReader:
				return
			case <-ka.C:
				kctx, kcancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := u.broker.client.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(kctx); err != nil {
					log.Printf("binance futures UDS: keepalive: %v", err)
				}
				kcancel()
			}
		}
	}()

	log.Printf("binance futures UDS: connected %s", u.broker.opts.wsBase())
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		u.dispatch(raw)
	}
}

func (u *futuresUserDataStream) dispatch(raw []byte) {
	var head struct {
		E string `json:"e"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return
	}
	switch head.E {
	case "ORDER_TRADE_UPDATE":
		u.handleOrderTradeUpdate(raw)
	case "ACCOUNT_UPDATE":
		u.handleAccountUpdate(raw)
	case "listenKeyExpired":
		log.Printf("binance futures UDS: listenKey expired; reconnecting")
		// 返回上层 run 循环重连由读错误触发：主动关不了 conn（无引用），靠交易所随后断开。
	}
}

// ORDER_TRADE_UPDATE：订单/成交回报（字段见 Binance USDT-M UDS 文档，o 为嵌套对象）。
func (u *futuresUserDataStream) handleOrderTradeUpdate(msg []byte) {
	var r struct {
		EventTime int64 `json:"E"`
		Order     struct {
			Symbol        string `json:"s"`
			Side          string `json:"S"`
			OrderID       int64  `json:"i"`
			Status        string `json:"X"`
			ExecType      string `json:"x"`
			LastFillQty   string `json:"l"`
			LastFillPrice string `json:"L"`
			CumFilledQty  string `json:"z"`
			Fee           string `json:"n"`
			TradeID       int64  `json:"t"`
		} `json:"o"`
	}
	if err := json.Unmarshal(msg, &r); err != nil {
		return
	}

	brokerOID := strconv.FormatInt(r.Order.OrderID, 10)
	ctx := context.Background()
	local, err := u.broker.store.GetOrderByBrokerOrderID(ctx, brokerOID)
	if err != nil {
		return // 外部下单（不经本服务）忽略
	}

	status := mapBinanceStatusString(r.Order.Status)
	filled, _ := strconv.ParseFloat(r.Order.CumFilledQty, 64)
	if err := u.broker.store.UpdateOrderFill(ctx, local.ID, status, filled); err != nil {
		log.Printf("binance futures UDS: update order %d: %v", local.ID, err)
	}

	if r.Order.ExecType == "TRADE" {
		lp, _ := strconv.ParseFloat(r.Order.LastFillPrice, 64)
		lq, _ := strconv.ParseFloat(r.Order.LastFillQty, 64)
		fee, _ := strconv.ParseFloat(r.Order.Fee, 64)
		side := "buy"
		if r.Order.Side == "SELL" {
			side = "sell"
		}
		tradedAt := time.UnixMilli(r.EventTime)
		if err := u.broker.store.InsertTradeFull(ctx, local.ID, r.Order.TradeID, local.Symbol, side, lp, lq, fee, tradedAt); err != nil {
			log.Printf("binance futures UDS: insert trade for order %d: %v", local.ID, err)
		}
		if u.broker.hub != nil {
			u.broker.hub.BroadcastEvent("trade", map[string]any{
				"orderId":  local.ID,
				"symbol":   local.Symbol,
				"side":     side,
				"price":    lp,
				"quantity": lq,
				"fee":      fee,
				"tradedAt": tradedAt,
			})
		}
	}
	u.broker.broadcastOrder(local.ID)
}

// ACCOUNT_UPDATE：余额 + 签名仓位（a.B / a.P）。交易所即真相，直接覆盖本地。
func (u *futuresUserDataStream) handleAccountUpdate(msg []byte) {
	var r struct {
		Data struct {
			Balances []struct {
				Asset   string `json:"a"`
				Balance string `json:"wb"` // wallet balance
			} `json:"B"`
			Positions []struct {
				Symbol     string `json:"s"`
				Amount     string `json:"pa"` // 签名仓位
				EntryPrice string `json:"ep"`
			} `json:"P"`
		} `json:"a"`
	}
	if err := json.Unmarshal(msg, &r); err != nil {
		return
	}
	ctx := context.Background()
	for _, bal := range r.Data.Balances {
		if bal.Asset != "USDT" {
			continue
		}
		wb, _ := strconv.ParseFloat(bal.Balance, 64)
		_ = u.broker.store.SetAccountCash(ctx, u.broker.accountID, wb)
	}
	for _, p := range r.Data.Positions {
		sym, ok := perpFromNative(p.Symbol)
		if !ok {
			continue
		}
		amt, _ := strconv.ParseFloat(p.Amount, 64)
		entry, _ := strconv.ParseFloat(p.EntryPrice, 64)
		_ = u.broker.store.UpsertPositionRaw(ctx, u.broker.accountID, sym, amt, entry)
	}
}
