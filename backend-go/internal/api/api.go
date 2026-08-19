// Package api 提供 HTTP 路由与处理器。
package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jiangbohhh/candleforge/backend-go/internal/broker"
	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
	"github.com/jiangbohhh/candleforge/backend-go/internal/market"
	"github.com/jiangbohhh/candleforge/backend-go/internal/risk"
	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/strategy"
	"github.com/jiangbohhh/candleforge/backend-go/internal/ws"
	"github.com/jiangbohhh/candleforge/backend-go/pb"
)

// Server 持有 HTTP 处理器所需的依赖。
type Server struct {
	db      *sql.DB
	store   *store.Store
	quant   *grpcclient.QuantClient
	src     market.MarketDataSource
	live    *market.LiveFeed
	hub     *ws.Hub
	brokers map[string]broker.Broker // account.broker → Broker 实例（主账户）
	risk    *risk.Engine
	envInfo map[string]string
	mgr     *strategy.Manager // M6 策略引擎（可为 nil，向后兼容）

	// 安全（WP2）
	authToken   string   // 静态 API Token；空 → 鉴权关闭
	corsOrigins []string // CORS 白名单；空 → 反射任意来源
}

// New 构造 API server。brokers 至少包含 "sim"。如果 BinanceBroker 启用则加 "binance"。
func New(db *sql.DB, st *store.Store, quant *grpcclient.QuantClient,
	src market.MarketDataSource, live *market.LiveFeed, hub *ws.Hub,
	brokers map[string]broker.Broker, riskEngine *risk.Engine, envInfo map[string]string) *Server {
	return &Server{
		db: db, store: st, quant: quant, src: src, live: live, hub: hub,
		brokers: brokers, risk: riskEngine, envInfo: envInfo,
	}
}

// SetManager 注入 M6 策略引擎（在 main.go 组装完成后调用）。
func (s *Server) SetManager(mgr *strategy.Manager) {
	s.mgr = mgr
}

// SetSecurity 注入鉴权 token 与 CORS 白名单（在 main.go 组装完成后调用）。
// authToken 为空则鉴权关闭；corsOrigins 为空则反射任意来源（均仅供本地开发）。
func (s *Server) SetSecurity(authToken string, corsOrigins []string) {
	s.authToken = authToken
	s.corsOrigins = corsOrigins
}

// Router 构建并返回 Gin 路由。
func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.Use(cors(s.corsOrigins))
	r.Use(auth(s.authToken))

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

	// 模拟交易 (M3) + 实盘 (M4)
	r.GET("/api/brokers", s.listBrokers)
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

	// M6 策略引擎
	r.GET("/api/strategies/schemas", s.listStrategySchemas)    // 须在 /:id 之前
	r.GET("/api/strategies", s.listStrategies)
	r.POST("/api/strategies", s.createStrategy)
	r.GET("/api/strategies/:id", s.getStrategy)
	r.DELETE("/api/strategies/:id", s.deleteStrategy)
	r.POST("/api/strategies/:id/start", s.startStrategy)
	r.POST("/api/strategies/:id/stop", s.stopStrategy)
	r.POST("/api/strategies/:id/transfer", s.transferStrategyFunds)

	// 子账户注册（live）
	r.POST("/api/accounts/sub", s.registerSubAccount)
	r.GET("/api/accounts/sub", s.listSubAccounts)

	// 前端实时 WS
	r.GET("/ws", func(c *gin.Context) {
		s.hub.HandleWS(c.Writer, c.Request)
	})

	return r
}

// cors 返回 CORS 中间件。origins 为空时反射任意来源（仅本地开发）；
// 非空时只对白名单内的 Origin 放行，并回显具体来源（Token 鉴权下不能用 "*"）。
func cors(origins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		switch {
		case len(origins) == 0:
			// 开发模式：反射来源（若无 Origin 则用 *），并允许携带凭证。
			if origin != "" {
				c.Header("Access-Control-Allow-Origin", origin)
			} else {
				c.Header("Access-Control-Allow-Origin", "*")
			}
		case origin != "":
			if _, ok := allowed[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			}
			// 不在白名单：不设 Allow-Origin，浏览器据此拦截。
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Auth-Token")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// auth 返回静态 Token 鉴权中间件。token 为空则放行（开发模式）。
// 已配置时校验 Authorization: Bearer <token> / X-Auth-Token 头 / ?token= 查询参数
// （查询参数用于浏览器 WebSocket，无法自定义握手头）。/healthz 与预检始终放行。
func auth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" || c.Request.Method == http.MethodOptions || c.Request.URL.Path == "/healthz" {
			c.Next()
			return
		}
		got := extractToken(c)
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
			return h[7:]
		}
		return h
	}
	if h := c.GetHeader("X-Auth-Token"); h != "" {
		return h
	}
	return c.Query("token")
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
		Symbol      string         `json:"symbol"`
		Strategy    string         `json:"strategy"`
		Interval    string         `json:"interval"`
		Params      map[string]any `json:"params"` // 值可为字符串（旧表单）或原生类型
		InitialCash float64        `json:"initialCash"`
		Commission  float64        `json:"commission"`
		Limit       int            `json:"limit"`
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

	// 双引擎路由：注册表中有 Go 回测函数 → 用 Go 引擎；否则走 Python gRPC。
	var resp *pb.BacktestResponse
	paramsJSON := normalizeParams(body.Params)
	if d := strategy.Lookup(body.Strategy); d != nil && d.Backtest != nil {
		// 永续标的取回测窗口内的资金费率历史（现货为空）
		var funding []store.FundingRate
		if market.IsPerp(body.Symbol) {
			last := klines[len(klines)-1]
			funding, err = s.store.ListFundingRates(ctx, body.Symbol, klines[0].OpenTime, last.CloseTime)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		resp, err = d.Backtest(klines, funding, paramsJSON, body.InitialCash, body.Commission)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "backtest failed: " + err.Error()})
			return
		}
	} else {
		pbKlines := make([]*pb.Kline, len(klines))
		for i, k := range klines {
			pbKlines[i] = &pb.Kline{
				OpenTime: k.OpenTime, Open: k.Open, High: k.High,
				Low: k.Low, Close: k.Close, Volume: k.Volume,
			}
		}
		resp, err = s.quant.RunBacktest(ctx, &pb.BacktestRequest{
			Symbol: body.Symbol, Strategy: body.Strategy, Params: stringifyParams(body.Params),
			InitialCash: body.InitialCash, Commission: body.Commission, Klines: pbKlines,
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "backtest failed: " + err.Error()})
			return
		}
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

// normalizeParams 把参数值归一化为原生 JSON 类型（旧表单发字符串数字，Go 引擎需要 float/bool）。
func normalizeParams(in map[string]any) json.RawMessage {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				out[k] = f
				continue
			}
			if b, err := strconv.ParseBool(s); err == nil {
				out[k] = b
				continue
			}
		}
		out[k] = v
	}
	b, _ := json.Marshal(out)
	return b
}

// stringifyParams 把参数值转为字符串（Python gRPC 契约是 map<string,string>）。
func stringifyParams(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = fmt.Sprint(v)
	}
	return out
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

// ── 模拟交易 (M3) + 实盘 (M4) ──

// resolveAccount 把请求中的 ?account=default|live 解析成 (*Account, Broker)。
// 缺省走 sim 账户，向后兼容 M3 调用。
func (s *Server) resolveAccount(c *gin.Context) (*store.Account, broker.Broker, error) {
	name := c.Query("account")
	if name == "" {
		name = "default"
	}
	ctx := c.Request.Context()

	var acct *store.Account
	var err error
	if name == "default" {
		// 兜底创建，保证 sim 账户始终可用
		acct, err = s.store.EnsureSimAccount(ctx, "default", 100000)
	} else {
		acct, err = s.store.GetAccountByName(ctx, name)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("account %q not found: %w", name, err)
	}
	br, ok := s.brokers[acct.Broker]
	if !ok {
		return nil, nil, fmt.Errorf("no broker available for %q (kind=%s)", name, acct.Broker)
	}
	return acct, br, nil
}

func (s *Server) priceLookup(symbol string) float64 {
	for _, t := range s.live.Snapshot() {
		if t.Symbol == symbol {
			return t.Price
		}
	}
	return 0
}

// BrokerInfo 是 /api/brokers 返回项。
type BrokerInfo struct {
	Name      string `json:"name"`      // default / live
	Kind      string `json:"kind"`      // sim / live
	Broker    string `json:"broker"`    // sim / binance
	Available bool   `json:"available"` // brokers map 里是否有对应实例
	Env       string `json:"env,omitempty"`
}

func (s *Server) listBrokers(c *gin.Context) {
	accounts, err := s.store.ListAccounts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 兜底：若 default 还没创建（首次启动），强制创建一次再列。
	if !hasAccountNamed(accounts, "default") {
		_, _ = s.store.EnsureSimAccount(c.Request.Context(), "default", 100000)
		accounts, _ = s.store.ListAccounts(c.Request.Context())
	}
	out := make([]BrokerInfo, 0, len(accounts))
	for _, a := range accounts {
		_, ok := s.brokers[a.Broker]
		info := BrokerInfo{
			Name: a.Name, Kind: a.Kind, Broker: a.Broker, Available: ok,
		}
		if env, ok := s.envInfo[a.Broker]; ok {
			info.Env = env
		}
		out = append(out, info)
	}
	c.JSON(http.StatusOK, out)
}

func hasAccountNamed(list []store.Account, name string) bool {
	for _, a := range list {
		if a.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) getAccount(c *gin.Context) {
	acct, _, err := s.resolveAccount(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, acct)
}

func (s *Server) getAccountSummary(c *gin.Context) {
	acct, _, err := s.resolveAccount(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
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
	acct, br, err := s.resolveAccount(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
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

	// Risk check (sim 和 live 共用同一引擎)
	if err := s.risk.Validate(c.Request.Context(), acct.ID,
		body.Symbol, body.Side, body.Type, body.Price, body.Quantity, s.priceLookup); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ord, err := br.PlaceOrder(c.Request.Context(), broker.PlaceOrderRequest{
		AccountID: acct.ID,
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
	acct, br, err := s.resolveAccount(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err := br.CancelOrder(c.Request.Context(), acct.ID, orderID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) listOrders(c *gin.Context) {
	acct, br, err := s.resolveAccount(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	orders, err := br.ListOrders(c.Request.Context(), acct.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, orders)
}

func (s *Server) listPositions(c *gin.Context) {
	acct, br, err := s.resolveAccount(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	positions, err := br.GetPositions(c.Request.Context(), acct.ID)
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
	acct, _, err := s.resolveAccount(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	trades, err := s.store.ListTrades(c.Request.Context(), acct.ID, 100)
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
