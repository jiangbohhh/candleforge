// strategies.go — /api/strategies* 和 /api/accounts/sub 路由处理器。
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/jiangbohhh/candleforge/backend-go/internal/store"
	"github.com/jiangbohhh/candleforge/backend-go/internal/strategy"
)

// ── /api/strategies/schemas ──

func (s *Server) listStrategySchemas(c *gin.Context) {
	c.JSON(http.StatusOK, strategy.AllSchemas())
}

// ── /api/strategies ──

func (s *Server) listStrategies(c *gin.Context) {
	rows, err := s.store.ListStrategies(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) createStrategy(c *gin.Context) {
	var body struct {
		Name       string          `json:"name"`
		Kind       string          `json:"kind"`
		AccountID  int64           `json:"accountId"`
		Symbol     string          `json:"symbol"`
		MarketType string          `json:"marketType"`
		Direction  string          `json:"direction"`
		Leverage   float64         `json:"leverage"`
		Params     json.RawMessage `json:"params"`
		// sim 专属：自动建虚拟子账户
		ParentAccountName string  `json:"parentAccount"` // 父账户名（sim 模式）
		Allocation        float64 `json:"allocation"`    // 划拨金额
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Name == "" || body.Kind == "" || body.Symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, kind, symbol required"})
		return
	}

	d := strategy.Lookup(body.Kind)
	if d == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown strategy kind: " + body.Kind})
		return
	}

	rawParams := body.Params
	if len(rawParams) == 0 {
		rawParams = json.RawMessage("{}")
	}
	// symbol ↔ 参数一致性校验（如 marketType=futures 必须用 .PERP 标的）
	if d.CheckSymbol != nil {
		if err := d.CheckSymbol(body.Symbol, rawParams); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	// 从参数同步 strategies 行的市场/方向列（参数是权威来源）
	var pmeta struct {
		MarketType string  `json:"marketType"`
		Direction  string  `json:"direction"`
		Leverage   float64 `json:"leverage"`
	}
	if err := json.Unmarshal(rawParams, &pmeta); err == nil {
		if pmeta.MarketType != "" {
			body.MarketType = pmeta.MarketType
		}
		if pmeta.Direction != "" {
			body.Direction = pmeta.Direction
		}
		if pmeta.Leverage != 0 {
			body.Leverage = pmeta.Leverage
		}
	}

	ctx := c.Request.Context()

	// 若未指定 accountId 且提供了 parentAccount，自动建虚拟子账户
	if body.AccountID == 0 && body.ParentAccountName != "" {
		parent, err := s.store.GetAccountByName(ctx, body.ParentAccountName)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parent account not found: " + body.ParentAccountName})
			return
		}
		subAcct, err := s.store.CreateVirtualSubAccount(ctx, parent.ID, body.Name+"-sub", body.Allocation)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		body.AccountID = subAcct.ID
	}

	// 填合约预留位默认值
	if body.MarketType == "" {
		body.MarketType = "spot"
	}
	if body.Direction == "" {
		body.Direction = "long"
	}
	if body.Leverage == 0 {
		body.Leverage = 1
	}

	params := rawParams

	id, err := s.store.InsertStrategy(ctx, store.StrategyRow{
		Name:       body.Name,
		Kind:       body.Kind,
		AccountID:  body.AccountID,
		Symbol:     body.Symbol,
		MarketType: body.MarketType,
		Direction:  body.Direction,
		Leverage:   body.Leverage,
		Params:     params,
		Status:     "stopped",
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	row, err := s.store.GetStrategy(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, row)
}

// ── /api/strategies/:id ──

func (s *Server) getStrategy(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return
	}
	row, err := s.store.GetStrategy(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, row)
}

func (s *Server) deleteStrategy(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return
	}
	ctx := c.Request.Context()
	row, err := s.store.GetStrategy(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if row.Status == "running" {
		c.JSON(http.StatusConflict, gin.H{"error": "stop the strategy before deleting"})
		return
	}
	if err := s.store.DeleteStrategy(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── /api/strategies/:id/start ──

func (s *Server) startStrategy(c *gin.Context) {
	if s.mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "strategy manager not available"})
		return
	}
	id, err := parseID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return
	}
	if err := s.mgr.StartStrategy(c.Request.Context(), id); err != nil {
		status := http.StatusInternalServerError
		msg := err.Error()
		if contains(msg, "already running") || contains(msg, "max running") || contains(msg, "already used") {
			status = http.StatusConflict
		} else if contains(msg, "not found") || contains(msg, "broker") {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": msg})
		return
	}
	row, _ := s.store.GetStrategy(c.Request.Context(), id)
	c.JSON(http.StatusOK, row)
}

// ── /api/strategies/:id/stop ──

func (s *Server) stopStrategy(c *gin.Context) {
	if s.mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "strategy manager not available"})
		return
	}
	id, err := parseID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return
	}
	var body struct {
		Liquidate bool `json:"liquidate"`
	}
	_ = c.ShouldBindJSON(&body)

	if err := s.mgr.StopStrategy(c.Request.Context(), id, body.Liquidate); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	row, _ := s.store.GetStrategy(c.Request.Context(), id)
	c.JSON(http.StatusOK, row)
}

// ── /api/strategies/:id/transfer ──

func (s *Server) transferStrategyFunds(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return
	}
	var body struct {
		Amount float64 `json:"amount"` // >0 = 父→子追加, <0 = 子→父回收（仅 stopped）
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Amount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount required"})
		return
	}
	ctx := c.Request.Context()
	row, err := s.store.GetStrategy(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if body.Amount < 0 && row.Status == "running" {
		c.JSON(http.StatusConflict, gin.H{"error": "stop strategy before recalling funds"})
		return
	}
	if row.AccountID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "strategy has no sub-account"})
		return
	}
	sub, err := s.store.GetSubAccount(ctx, row.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sub account not found"})
		return
	}
	var fromID, toID int64
	amount := body.Amount
	reason := "allocate"
	if amount > 0 {
		fromID, toID = sub.ParentID, row.AccountID
	} else {
		fromID, toID = row.AccountID, sub.ParentID
		amount = -amount
		reason = "recall"
	}
	if err := s.store.TransferFunds(ctx, fromID, toID, amount, reason); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── /api/accounts/sub ──

func (s *Server) registerSubAccount(c *gin.Context) {
	var body struct {
		ParentAccount string `json:"parentAccount"`
		Name          string `json:"name"`
		SubEmail      string `json:"subEmail"`
		APIKey        string `json:"apiKey"`
		APISecret     string `json:"apiSecret"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Name == "" || body.APIKey == "" || body.APISecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, apiKey, apiSecret required"})
		return
	}
	ctx := c.Request.Context()
	parent, err := s.store.GetAccountByName(ctx, body.ParentAccount)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parent account not found"})
		return
	}
	sa, err := s.store.RegisterRealSubAccount(ctx, parent.ID, body.Name, body.SubEmail, body.APIKey, body.APISecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 不回显凭证
	c.JSON(http.StatusCreated, gin.H{
		"id": sa.ID, "name": sa.Name, "kind": sa.Kind, "broker": sa.Broker,
		"parentId": sa.ParentID, "subEmail": sa.SubEmail, "cash": sa.Cash,
	})
}

func (s *Server) listSubAccounts(c *gin.Context) {
	name := c.DefaultQuery("parent", "live")
	ctx := c.Request.Context()
	parent, err := s.store.GetAccountByName(ctx, name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "parent account not found"})
		return
	}
	subs, err := s.store.ListSubAccounts(ctx, parent.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 不回显凭证
	out := make([]gin.H, len(subs))
	for i, sa := range subs {
		out[i] = gin.H{
			"id": sa.ID, "name": sa.Name, "kind": sa.Kind, "broker": sa.Broker,
			"parentId": sa.ParentID, "subEmail": sa.SubEmail, "cash": sa.Cash,
		}
	}
	c.JSON(http.StatusOK, out)
}

// ── helpers ──

func parseID(c *gin.Context, param string) (int64, error) {
	return strconv.ParseInt(c.Param(param), 10, 64)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
