// Package api 提供 HTTP 路由与处理器。
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jiangbohhh/candleforge/backend-go/internal/broker"
	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/risk"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
	"github.com/jiangbohhh/candleforge/backend-go/pb"
)

// Server 持有 HTTP 处理器所需的依赖。
type Server struct {
	db     *sql.DB
	store  *store.Store
	quant  *grpcclient.QuantClient
	src    market.MarketDataSource
	live   *market.LiveFeed
	hub    *ws.Hub
	broker broker.Broker
	risk   *risk.Engine
}

// New 构造 API server。
func New(db *sql.DB, st *store.Store, quant *grpcclient.QuantClient,
	src market.MarketDataSource, live *market.LiveFeed, hub *ws.Hub,
	br broker.Broker, riskEngine *risk.Engine) *Server {
	return &Server{db: db, store: st, quant: quant, src: src, live: live, hub: hub, broker: br, risk: riskEngine}
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

	// 回测
	r.POST("/api/backtest", s.runBacktest)
	r.GET("/api/backtest/runs", s.listBacktestRuns)
	r.GET("/api/backtest/runs/:id", s.getBacktestRun)

	// 管理
	r.POST("/api/admin/backfill", s.triggerBackfill)

	// 模拟交易 (M3)
	r.GET("/api/account", s.getAccount)
	r.GET("/api/account/summary", s.getAccountSummary)
	r.POST("/api/orders", s.placeOrder)
	r.DELETE("/api/orders/:id", s.cancelOrder)
	r.GET("/api/orders", s.listOrders)
	r.GET("/api/positions", s.listPositions)
	r.GET("/api/trades", s.listTrades)

	// 风控
	r.GET("/api/risk/status", s.getRiskStatus)
	r.POST("/api/risk/halt", s.setRiskHalt)

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

// ── 回测 ──

// protoMarshaler 用 protojson 输出干净 JSON（camelCase，无内部字段）。
var protoMarshaler = protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: false}

func (s *Server) runBacktest(c *gin.Context) {
	var body struct {
		Symbol      string            `json:"symbol"`
		Strategy    string            `json:"strategy"`
		Interval    string            `json:"interval"`
		Params      map[string]string `json:"params"`
		InitialCash float64           `json:"initialCash"`
		Commission  float64           `json:"commission"`
		Limit       int               `json:"limit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Symbol == "" || body.Strategy == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol and strategy required"})
		return
	}
	if body.Interval == "" {
		body.Interval = "1h"
	}
	if body.Limit <= 0 {
		body.Limit = 500
	}
	if body.InitialCash <= 0 {
		body.InitialCash = 10000
	}

	ctx := c.Request.Context()
	klines, err := s.store.GetKlines(ctx, body.Symbol, body.Interval, body.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(klines) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no klines for symbol/interval; run backfill first"})
		return
	}

	pbKlines := make([]*pb.Kline, len(klines))
	for i, k := range klines {
		pbKlines[i] = &pb.Kline{
			OpenTime: k.OpenTime, Open: k.Open, High: k.High,
			Low: k.Low, Close: k.Close, Volume: k.Volume,
		}
	}

	resp, err := s.quant.RunBacktest(ctx, &pb.BacktestRequest{
		Symbol: body.Symbol, Strategy: body.Strategy, Params: body.Params,
		InitialCash: body.InitialCash, Commission: body.Commission, Klines: pbKlines,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "backtest failed: " + err.Error()})
		return
	}

	// protojson 序列化各部分用于落库与返回
	metricsJSON, _ := protoMarshaler.Marshal(resp.GetMetrics())
	equityJSON := marshalList(len(resp.GetEquityCurve()), func(i int) any {
		b, _ := protoMarshaler.Marshal(resp.GetEquityCurve()[i])
		return json.RawMessage(b)
	})
	tradesJSON := marshalList(len(resp.GetTrades()), func(i int) any {
		b, _ := protoMarshaler.Marshal(resp.GetTrades()[i])
		return json.RawMessage(b)
	})
	paramsJSON, _ := json.Marshal(body.Params)

	id, err := s.store.InsertBacktestRun(ctx, store.BacktestRun{
		Symbol: body.Symbol, Strategy: body.Strategy, Interval: body.Interval,
		Params: paramsJSON, Metrics: metricsJSON,
		EquityCurve: equityJSON, Trades: tradesJSON,
		InitialCash: body.InitialCash, Commission: body.Commission,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          id,
		"symbol":      body.Symbol,
		"strategy":    body.Strategy,
		"interval":    body.Interval,
		"metrics":     json.RawMessage(metricsJSON),
		"equityCurve": json.RawMessage(equityJSON),
		"trades":      json.RawMessage(tradesJSON),
	})
}

func (s *Server) listBacktestRuns(c *gin.Context) {
	runs, err := s.store.ListBacktestRuns(c.Request.Context(), 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, runs)
}

func (s *Server) getBacktestRun(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return
	}
	run, err := s.store.GetBacktestRun(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, run)
}

// marshalList 把 n 个元素拼成 JSON 数组的 RawMessage。
func marshalList(n int, get func(i int) any) json.RawMessage {
	items := make([]any, n)
	for i := 0; i < n; i++ {
		items[i] = get(i)
	}
	b, _ := json.Marshal(items)
	return b
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

// ── 模拟交易 (M3) ──

func (s *Server) simAccountID(c *gin.Context) (int64, error) {
	ctx := c.Request.Context()
	acct, err := s.store.EnsureSimAccount(ctx, "default", 100000)
	if err != nil {
		return 0, err
	}
	return acct.ID, nil
}

func (s *Server) priceLookup(symbol string) float64 {
	for _, t := range s.live.Snapshot() {
		if t.Symbol == symbol {
			return t.Price
		}
	}
	return 0
}

func (s *Server) getAccount(c *gin.Context) {
	acct, err := s.store.EnsureSimAccount(c.Request.Context(), "default", 100000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, acct)
}

func (s *Server) getAccountSummary(c *gin.Context) {
	acct, err := s.store.EnsureSimAccount(c.Request.Context(), "default", 100000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	summary, err := s.store.AccountSummary(c.Request.Context(), acct.ID, s.priceLookup)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, summary)
}

func (s *Server) placeOrder(c *gin.Context) {
	accountID, err := s.simAccountID(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var body struct {
		Symbol   string  `json:"symbol"`
		Side     string  `json:"side"`
		Type     string  `json:"type"`
		Price    float64 `json:"price"`
		Quantity float64 `json:"quantity"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Symbol == "" || body.Side == "" || body.Quantity <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol, side, quantity required"})
		return
	}
	if body.Type == "" {
		body.Type = "market"
	}

	// Risk check
	if err := s.risk.Validate(c.Request.Context(), accountID,
		body.Symbol, body.Side, body.Type, body.Price, body.Quantity, s.priceLookup); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ord, err := s.broker.PlaceOrder(c.Request.Context(), broker.PlaceOrderRequest{
		AccountID: accountID,
		Symbol:    body.Symbol,
		Side:      body.Side,
		OrderType: body.Type,
		Price:     body.Price,
		Quantity:  body.Quantity,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ord)
}

func (s *Server) cancelOrder(c *gin.Context) {
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad order id"})
		return
	}
	accountID, err := s.simAccountID(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := s.broker.CancelOrder(c.Request.Context(), accountID, orderID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) listOrders(c *gin.Context) {
	accountID, err := s.simAccountID(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	orders, err := s.broker.ListOrders(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, orders)
}

func (s *Server) listPositions(c *gin.Context) {
	accountID, err := s.simAccountID(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	positions, err := s.broker.GetPositions(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for i := range positions {
		positions[i].MarketPrice = s.priceLookup(positions[i].Symbol)
		positions[i].Unrealized = (positions[i].MarketPrice - positions[i].AvgPrice) * positions[i].Quantity
	}
	c.JSON(http.StatusOK, positions)
}

func (s *Server) listTrades(c *gin.Context) {
	accountID, err := s.simAccountID(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	trades, err := s.store.ListTrades(c.Request.Context(), accountID, 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, trades)
}

// ── 风控 ──

func (s *Server) getRiskStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.risk.Status())
}

func (s *Server) setRiskHalt(c *gin.Context) {
	var body struct {
		Halt   bool   `json:"halt"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Halt {
		reason := body.Reason
		if reason == "" {
			reason = "manual"
		}
		s.risk.Halt(reason)
	} else {
		s.risk.Unhalt()
	}
	c.JSON(http.StatusOK, s.risk.Status())
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
