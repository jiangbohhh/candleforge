// Package api 提供 HTTP 路由与处理器。
package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
)

// Server 持有 HTTP 处理器所需的依赖。
type Server struct {
	db    *sql.DB
	store *store.Store
	quant *grpcclient.QuantClient
	src   market.MarketDataSource
	live  *market.LiveFeed
	hub   *ws.Hub
}

// New 构造 API server。
func New(db *sql.DB, st *store.Store, quant *grpcclient.QuantClient,
	src market.MarketDataSource, live *market.LiveFeed, hub *ws.Hub) *Server {
	return &Server{db: db, store: st, quant: quant, src: src, live: live, hub: hub}
}

// Router 构建并返回 Gin 路由。
func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.Use(cors())

	r.GET("/healthz", s.healthz)
	r.GET("/api/ping-quant", s.pingQuant)

	// 行情
	r.GET("/api/symbols", s.listSymbols)
	r.GET("/api/klines", s.getKlines)
	r.GET("/api/tickers", s.getTickers)

	// 自选
	r.GET("/api/watchlist", s.getWatchlist)
	r.POST("/api/watchlist", s.addWatch)
	r.DELETE("/api/watchlist/:symbol", s.removeWatch)

	// 管理
	r.POST("/api/admin/backfill", s.triggerBackfill)

	// 前端实时 WS
	r.GET("/ws", func(c *gin.Context) {
		s.hub.HandleWS(c.Writer, c.Request)
	})

	return r
}

// cors 是开发期用的宽松 CORS 中间件。
func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// ── 健康检查 ──

func (s *Server) healthz(c *gin.Context) {
	status := gin.H{"status": "ok", "service": "candleforge-go"}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		status["database"] = "down: " + err.Error()
		c.JSON(http.StatusServiceUnavailable, status)
		return
	}
	status["database"] = "ok"
	c.JSON(http.StatusOK, status)
}

func (s *Server) pingQuant(c *gin.Context) {
	resp, err := s.quant.Ping(c.Request.Context(), "ping from go")
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"grpc": "down: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"grpc":             "ok",
		"reply":            resp.GetMessage(),
		"replying_service": resp.GetService(),
	})
}

// ── 行情 ──

func (s *Server) listSymbols(c *gin.Context) {
	syms, err := s.store.ListSymbols(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, syms)
}

func (s *Server) getKlines(c *gin.Context) {
	symbol := c.Query("symbol")
	interval := c.DefaultQuery("interval", "1h")
	limit := atoiDefault(c.Query("limit"), 500)
	if symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol required"})
		return
	}
	ks, err := s.store.GetKlines(c.Request.Context(), symbol, interval, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ks)
}

func (s *Server) getTickers(c *gin.Context) {
	c.JSON(http.StatusOK, s.live.Snapshot())
}

// ── 自选 ──

func (s *Server) getWatchlist(c *gin.Context) {
	list, err := s.store.ListWatchlist(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (s *Server) addWatch(c *gin.Context) {
	var body struct {
		Symbol string `json:"symbol"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol required"})
		return
	}
	if err := s.store.AddWatch(c.Request.Context(), body.Symbol); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) removeWatch(c *gin.Context) {
	symbol := c.Param("symbol")
	if err := s.store.RemoveWatch(c.Request.Context(), symbol); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── 管理 ──

func (s *Server) triggerBackfill(c *gin.Context) {
	// 异步执行，避免请求阻塞
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = market.Backfill(ctx, s.store, s.src)
	}()
	c.JSON(http.StatusAccepted, gin.H{"status": "backfill started"})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
