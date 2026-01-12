// Package grpc 提供与 Tetragon gRPC 服务交互的客户端功能
// 包括连接管理、TLS 配置和事件流处理
package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Client gRPC 客户端
type Client struct {
	conn   *grpc.ClientConn
	client tetragon.FineGuidanceSensorsClient
	config *config.TetragonConfig
}

// NewClient 创建新的 gRPC 客户端
func NewClient(cfg *config.TetragonConfig) (*Client, error) {
	var opts []grpc.DialOption

	// TLS 配置
	if cfg.TLS.Enabled {
		tlsConfig, err := loadTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// 连接（使用 DialContext 支持超时控制）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, cfg.GRPCAddr, opts...)
	if err != nil {
		return nil, err
	}

	client := tetragon.NewFineGuidanceSensorsClient(conn)

	return &Client{
		conn:   conn,
		client: client,
		config: cfg,
	}, nil
}

// GetEventsClient 获取 GetEvents 流客户端
func (c *Client) GetEventsClient(ctx context.Context) (tetragon.FineGuidanceSensors_GetEventsClient, error) {
	return c.client.GetEvents(ctx, &tetragon.GetEventsRequest{})
}

// Close 关闭连接
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// loadTLSConfig 加载 TLS 配置
func loadTLSConfig(cfg config.TLSConfig) (*tls.Config, error) {
	tlsConfig := &tls.Config{}

	// 加载 CA 证书
	if cfg.CACert != "" {
		caCert, err := os.ReadFile(cfg.CACert)
		if err != nil {
			return nil, err
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate from PEM: %s", cfg.CACert)
		}
		tlsConfig.RootCAs = caCertPool
	}

	// 加载客户端证书
	if cfg.ClientCert != "" && cfg.ClientKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCert, cfg.ClientKey)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}
