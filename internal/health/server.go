package health

import (
	"context"
	"encoding/json"
	"fmt"
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

	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/ready", s.readyHandler)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return s
}

// Start 启动健康检查服务器
func (s *Server) Start() error {
	s.logger.Info("正在启动健康检查服务器", 
		zap.String("地址", s.server.Addr),
		zap.Strings("端点", []string{"/health", "/ready"}))
	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.logger.Error("健康检查服务器启动失败", zap.Error(err))
		return err
	}
	s.logger.Info("健康检查服务器已成功启动并监听", zap.String("地址", s.server.Addr))
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

// healthHandler 健康检查处理器
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	
	status := map[string]interface{}{
		"status": "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"queue": map[string]interface{}{
			"depth":    s.queue.Size(),
			"capacity": s.queue.Capacity(),
		},
	}
	
	json.NewEncoder(w).Encode(status)
}

// readyHandler 就绪检查处理器
func (s *Server) readyHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	shutdown := s.shutdown
	ready := s.ready
	s.mu.RUnlock()
	
	// 如果正在关闭，返回未就绪
	if shutdown {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("shutting down"))
		return
	}
	
	// 检查服务是否标记为就绪
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready"))
		return
	}
	
	// 队列未满时认为就绪
	if s.queue.Size() < s.queue.Capacity() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready: queue full"))
	}
}

// SetReady 设置服务就绪状态
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
