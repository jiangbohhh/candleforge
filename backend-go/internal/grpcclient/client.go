// Package grpcclient 封装对 Python 回测服务的 gRPC 调用。
package grpcclient

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jiangbohhh/candleforge/backend-go/pb"
)

// QuantClient 是 Python 回测服务的客户端。
type QuantClient struct {
	conn   *grpc.ClientConn
	client pb.QuantServiceClient
}

// New 拨号连接 Python 回测服务。addr 形如 "localhost:50051"。
// 使用 lazy 连接：拨号本身不阻塞，首次 RPC 时才真正建连。
func New(addr string) (*QuantClient, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}
	return &QuantClient{
		conn:   conn,
		client: pb.NewQuantServiceClient(conn),
	}, nil
}

// Close 关闭底层连接。
func (c *QuantClient) Close() error {
	return c.conn.Close()
}

// Ping 调用 Python 端健康检查，验证 gRPC 链路连通。
func (c *QuantClient) Ping(ctx context.Context, msg string) (*pb.PingResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.client.Ping(ctx, &pb.PingRequest{Message: msg})
}
