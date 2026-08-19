// Package ws 提供面向前端的 WebSocket 广播。
package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// allowedOrigins 为 WS 升级的来源白名单；为空表示放开（仅本地开发）。
// 由 main.go 在启动时通过 SetAllowedOrigins 注入。
var allowedOrigins []string

// SetAllowedOrigins 配置 WS 升级允许的 Origin 白名单（CORS 收紧的一部分）。
func SetAllowedOrigins(origins []string) {
	allowedOrigins = origins
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		if len(allowedOrigins) == 0 {
			return true // 未配置白名单 → 开发模式放开
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false // 白名单模式下拒绝无 Origin 的跨源请求
		}
		for _, o := range allowedOrigins {
			if o == origin || o == "*" {
				return true
			}
		}
		return false
	},
}

// Hub 维护所有前端连接，并向其广播消息。
type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]struct{}
	broadcast  chan []byte
	register   chan *client
	unregister chan *client
}

type client struct {
	conn *websocket.Conn
	send chan []byte
}

func NewHub() *Hub {
	return &Hub{
		clients:    map[*client]struct{}{},
		broadcast:  make(chan []byte, 256),
		register:   make(chan *client),
		unregister: make(chan *client),
	}
}

// Run 启动 Hub 事件循环（在独立 goroutine 中调用）。
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
		case msg := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// 客户端发送缓冲满，丢弃（不阻塞广播）
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast 向所有前端连接推送一条消息（非阻塞）。
func (h *Hub) Broadcast(msg []byte) {
	select {
	case h.broadcast <- msg:
	default:
		// 广播缓冲满，丢弃本条
	}
}

// BroadcastEvent 包装 {type, data} 并调用 Broadcast，供各 broker 和策略引擎复用。
func (h *Hub) BroadcastEvent(eventType string, data any) {
	msg, err := json.Marshal(map[string]any{"type": eventType, "data": data})
	if err != nil {
		return
	}
	h.Broadcast(msg)
}

// HandleWS 是 gin 可用的 WebSocket 升级处理器。
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	c := &client{conn: conn, send: make(chan []byte, 64)}
	h.register <- c

	go c.writePump()
	go c.readPump(h)
}

// writePump 把 send 通道的消息写到连接。
func (c *client) writePump() {
	defer c.conn.Close()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

// readPump 读取（并丢弃）客户端消息，检测断开以触发注销。
func (c *client) readPump(h *Hub) {
	defer func() {
		h.unregister <- c
		c.conn.Close()
	}()
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}
