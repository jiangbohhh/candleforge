// Package broker — Binance Spot User Data Stream over WebSocket API.
//
// Binance 在 2026-02-04 下线了旧的 listenKey 体系
// (POST /api/v3/userDataStream / wss://stream.binance.com:9443/ws/<listenKey>).
// 新方式：连接 ws-api/v3，发送 userDataStream.subscribe.signature 请求
// （HMAC 签名 apiKey+timestamp），随后服务端推送的事件以
// {"subscriptionId":N,"event":{"e":"executionReport",...}} 形式返回。
//
// 单条 WS goroutine；断线指数退避重连；gorilla/websocket 自动回 pong。
package broker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
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

		if err := u.dialAndSubscribe(); err != nil {
			log.Printf("binance UDS: %v; reconnect in %s", err, backoff)
			if !sleepOrStop(u.stopCh, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
	}
}

func (u *userDataStream) dialAndSubscribe() error {
	wsURL := u.broker.opts.wsBase()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer conn.Close()

	// 给读循环一个停车位：stopCh 触发就关连接，让 ReadMessage 出错返回。
	stopReader := make(chan struct{})
	defer close(stopReader)
	go func() {
		select {
		case <-u.stopCh:
			_ = conn.Close()
		case <-stopReader:
		}
	}()

	// 发起 userDataStream.subscribe.signature
	reqID := uuid.New().String()
	ts := time.Now().UnixMilli()
	payload := fmt.Sprintf("apiKey=%s&timestamp=%d", u.broker.opts.APIKey, ts)
	sig := hmacSHA256Hex(u.broker.opts.APISecret, payload)

	req := map[string]any{
		"id":     reqID,
		"method": "userDataStream.subscribe.signature",
		"params": map[string]any{
			"apiKey":    u.broker.opts.APIKey,
			"timestamp": ts,
			"signature": sig,
		},
	}
	if err := conn.WriteJSON(req); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}

	log.Printf("binance UDS: subscribed to %s", wsURL)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		u.dispatch(raw, reqID)
	}
}

func (u *userDataStream) dispatch(raw []byte, subscribeReqID string) {
	// 三种消息：
	// 1) {"id":"<req>", "status":200, "result":{"subscriptionId":N}} — 订阅 ack
	// 2) {"id":"<req>", "status":XXX, "error":{...}} — 错误
	// 3) {"subscriptionId":N, "event":{"e":"...", ...}} — 推送事件
	var generic struct {
		ID             string          `json:"id"`
		Status         int             `json:"status"`
		Result         json.RawMessage `json:"result"`
		Error          json.RawMessage `json:"error"`
		SubscriptionID *int64          `json:"subscriptionId"`
		Event          json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return
	}

	if generic.ID == subscribeReqID {
		if generic.Status != 200 {
			log.Printf("binance UDS: subscribe rejected: %s", string(generic.Error))
			return
		}
		log.Printf("binance UDS: subscribe ack %s", string(generic.Result))
		return
	}

	if len(generic.Event) == 0 {
		return // 既不是 ack 也不是 event（比如 session.status 回执），忽略。
	}

	var head struct {
		E string `json:"e"`
	}
	if err := json.Unmarshal(generic.Event, &head); err != nil {
		return
	}
	switch head.E {
	case "executionReport":
		u.handleExecutionReport(generic.Event)
	case "outboundAccountPosition":
		u.handleAccountUpdate(generic.Event)
	}
}

// executionReport 字段：参见 Binance Spot WS API docs。
// i=orderId, X=status, x=execType (NEW/TRADE/CANCELED...),
// l=last fill qty, L=last fill price, z=cumulative filled qty, n=commission,
// S=side, s=symbol(native), E=event time。
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
		// 外部下单（不经本服务）忽略。
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

func hmacSHA256Hex(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
