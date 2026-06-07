// Package api 提供 HTTP 路由与处理器。
package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jiangbohhh/candleforge/backend-go/internal/grpcclient"
)

// Server 持有 HTTP 处理器所需的依赖。
type Server struct {
	db    *sql.DB
	quant *grpcclient.QuantClient
}

// New 构造 API server。
func New(db *sql.DB, quant *grpcclient.QuantClient) *Server {
	return &Server{db: db, quant: quant}
}

// Router 构建并返回 Gin 路由。
func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.Use(cors())

	r.GET("/healthz", s.healthz)
	r.GET("/api/ping-quant", s.pingQuant)

	return r
}

// cors 是开发期用的宽松 CORS 中间件。
// 生产环境应收紧 Allow-Origin（M0 阶段先放开）。
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

// healthz 基础存活检查 + 数据库连通性。
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

// pingQuant 验证 Go -> Python gRPC 链路。
func (s *Server) pingQuant(c *gin.Context) {
	resp, err := s.quant.Ping(c.Request.Context(), "ping from go")
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"grpc": "down: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"grpc":            "ok",
		"reply":           resp.GetMessage(),
		"replying_service": resp.GetService(),
	})
}
