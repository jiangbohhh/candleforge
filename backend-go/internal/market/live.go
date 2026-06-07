package market

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

// LiveFeed 订阅实时行情，维护最新价缓存，并经 WS Hub 广播给前端。
type LiveFeed struct {
	src  MarketDataSource
	hub  *ws.Hub
	mu   sync.RWMutex
	last map[string]Tick // 统一 symbol -> 最新 tick
	stop func()
}

func NewLiveFeed(src MarketDataSource, hub *ws.Hub) *LiveFeed {
	return &LiveFeed{
		src:  src,
		hub:  hub,
		last: map[string]Tick{},
	}
}

// Start 订阅预置标的的实时行情。
func (l *LiveFeed) Start(ctx context.Context) {
	symbols := WatchedSymbols()
	stop, err := l.src.SubscribeTicker(symbols, l.onTick)
	if err != nil {
		log.Printf("live feed subscribe failed: %v", err)
		return
	}
	l.stop = stop
	log.Printf("live feed started for %d symbols", len(symbols))
}

// Stop 停止订阅。
func (l *LiveFeed) Stop() {
	if l.stop != nil {
		l.stop()
	}
}

func (l *LiveFeed) onTick(t Tick) {
	l.mu.Lock()
	l.last[t.Symbol] = t
	l.mu.Unlock()

	// 广播给前端
	msg, err := json.Marshal(wsMessage{Type: "ticker", Data: t})
	if err == nil {
		l.hub.Broadcast(msg)
	}
}

// Snapshot 返回所有最新价快照（供 REST /api/tickers）。
func (l *LiveFeed) Snapshot() []Tick {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Tick, 0, len(l.last))
	for _, t := range l.last {
		out = append(out, t)
	}
	return out
}

type wsMessage struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}
