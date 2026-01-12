package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/yourorg/tetragon-kafka-adapter/internal/queue"
	"go.uber.org/zap"
)

// Server 健康检查服务器
type Server struct {
	queue    *queue.Queue
	logger   *zap.Logger
	server   *http.Server
	mu       sync.RWMutex // 保护 ready 和 shutdown 状态
	ready    bool
	shutdown bool
}

// NewServer 创建新的健康检查服务器
func NewServer(port int, q *queue.Queue, logger *zap.Logger) *Server {
	mux := http.NewServeMux()
	
	s := &Server{
		queue:  q,
		logger: logger,
	}

	// 注册健康检查端点（必须在根路径之前注册，确保精确匹配）
	mux.HandleFunc("/health", s.healthHandler)
	// 添加根路径处理器，用于调试（放在最后，作为 fallback）
	mux.HandleFunc("/", s.rootHandler)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	logger.Info("健康检查服务器已创建",
		zap.Int("端口", port),
		zap.String("地址", fmt.Sprintf(":%d", port)),
		zap.Strings("端点", []string{"/", "/health"}))

	return s
}

// Start 启动健康检查服务器
func (s *Server) Start() error {
	s.logger.Info("正在启动健康检查服务器", 
		zap.String("地址", s.server.Addr),
		zap.Strings("端点", []string{"/", "/health"}))
	
	// 在 goroutine 中启动，以便可以立即返回并记录启动状态
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		s.logger.Error("健康检查服务器监听失败", zap.Error(err))
		return err
	}
	
	s.logger.Info("健康检查服务器已成功监听",
		zap.String("地址", listener.Addr().String()),
		zap.Strings("端点", []string{"/", "/health"}))
	
	// 启动服务器
	if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
		s.logger.Error("健康检查服务器启动失败", zap.Error(err))
		return err
	}
	
	return nil
}

// Shutdown 关闭服务器
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("正在关闭健康检查服务器")
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Error("关闭健康检查服务器失败", zap.Error(err))
		return err
	}
	s.logger.Info("健康检查服务器已关闭")
	return nil
}

// rootHandler 根路径处理器（用于调试）
// 注意：这个处理器只处理精确匹配 "/" 的请求，不会拦截其他路径
func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
	// 如果不是根路径，返回 404
	if r.URL.Path != "/" {
		s.logger.Warn("收到未匹配的请求",
			zap.String("方法", r.Method),
			zap.String("路径", r.URL.Path),
			zap.String("远程地址", r.RemoteAddr))
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("404 page not found"))
		return
	}
	
	s.logger.Info("收到根路径请求",
		zap.String("方法", r.Method),
		zap.String("路径", r.URL.Path),
		zap.String("远程地址", r.RemoteAddr))
	
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	
	status := map[string]interface{}{
		"service": "tetragon-kafka-adapter",
		"endpoints": []string{"/health"},
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	
	if err := json.NewEncoder(w).Encode(status); err != nil {
		s.logger.Error("写入根路径响应失败", zap.Error(err))
	}
}

// healthHandler 健康检查处理器（同时用于 liveness 和 readiness probe）
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("收到健康检查请求",
		zap.String("方法", r.Method),
		zap.String("路径", r.URL.Path),
		zap.String("远程地址", r.RemoteAddr))
	
	s.mu.RLock()
	shutdown := s.shutdown
	ready := s.ready
	s.mu.RUnlock()
	
	// 如果正在关闭，返回未就绪
	if shutdown {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		status := map[string]interface{}{
			"status": "shutting down",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}
		if err := json.NewEncoder(w).Encode(status); err != nil {
			s.logger.Error("写入健康检查响应失败", zap.Error(err))
		}
		return
	}
	
	// 检查服务是否标记为就绪（用于 readiness probe）
	if !ready {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		status := map[string]interface{}{
			"status": "not ready",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}
		if err := json.NewEncoder(w).Encode(status); err != nil {
			s.logger.Error("写入健康检查响应失败", zap.Error(err))
		}
		return
	}
	
	// 确保设置正确的 Content-Type
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	
	status := map[string]interface{}{
		"status": "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"queue": map[string]interface{}{
			"depth":    s.queue.Size(),
			"capacity": s.queue.Capacity(),
		},
	}
	
	// 先设置状态码，再写入响应体
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(status); err != nil {
		s.logger.Error("写入健康检查响应失败", zap.Error(err))
	}
}

// SetReady 设置服务就绪状态（保留此函数以备将来使用）
func (s *Server) SetReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = ready
}

// SetShutdown 设置关闭状态
func (s *Server) SetShutdown(shutdown bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdown = shutdown
}
