package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/grpc"
	"github.com/yourorg/tetragon-kafka-adapter/internal/health"
	"github.com/yourorg/tetragon-kafka-adapter/internal/kafka"
	"github.com/yourorg/tetragon-kafka-adapter/internal/logger"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"github.com/yourorg/tetragon-kafka-adapter/internal/normalize"
	"github.com/yourorg/tetragon-kafka-adapter/internal/queue"
	"github.com/yourorg/tetragon-kafka-adapter/internal/router"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "", "Path to config file")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		panic(err)
	}

	// 验证配置
	if err := config.Validate(cfg); err != nil {
		panic(err)
	}

	// 第一步：初始化基础日志
	// 如果配置了 Kafka 日志输出，先临时禁用，等创建 Producer 后再启用
	// 如果只配置了 Kafka 输出，第一次初始化时不创建任何输出（避免文件权限问题）
	tempLoggerCfg := cfg.Logger
	hasKafkaLog := false
	onlyKafkaOutput := false
	
	for _, output := range tempLoggerCfg.Output {
		if output == "kafka" {
			hasKafkaLog = true
		}
	}
	
	// 检查是否只配置了 Kafka 输出（没有文件输出）
	if hasKafkaLog && len(tempLoggerCfg.Output) == 1 {
		onlyKafkaOutput = true
	}
	
	// 如果只配置了 Kafka 输出，第一次初始化时使用空输出（避免创建文件）
	if onlyKafkaOutput {
		tempLoggerCfg.Output = []string{} // 空输出，不创建文件
	} else {
		// 临时移除 kafka 输出，保留其他输出（如 file）
		var newOutputs []string
		for _, o := range tempLoggerCfg.Output {
			if o != "kafka" {
				newOutputs = append(newOutputs, o)
			}
		}
		if len(newOutputs) == 0 {
			newOutputs = []string{"file"} // 至少保留文件输出
		}
		tempLoggerCfg.Output = newOutputs
	}

	if err := logger.Init(&tempLoggerCfg, nil); err != nil {
		panic(err)
	}
	defer logger.Sync()

	log := logger.GetLogger()
	log.Info("正在启动 Tetragon Kafka Adapter",
		zap.String("grpc地址", cfg.Tetragon.GRPCAddr),
		zap.Strings("kafka代理", cfg.Kafka.Brokers))

	// 创建上下文和 WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup

	// 初始化 gRPC 客户端
	grpcClient, err := grpc.NewClient(&cfg.Tetragon)
	if err != nil {
		log.Fatal("创建 gRPC 客户端失败", zap.Error(err))
	}
	defer grpcClient.Close()

	// 创建队列
	eventQueue := queue.NewQueue(&cfg.Tetragon.Stream, log)
	defer eventQueue.Close()
	sampler := queue.NewSampler(&cfg.Tetragon.Stream)

	// 创建路由
	r := router.NewRouter(cfg.Routing.Topics, log)

	// 创建规范化器
	normalizer := normalize.NewEventNormalizer(log)

	// 创建 Kafka Topic Admin（方案 1：自动创建 Compacted Topic）
	var topicAdmin *kafka.TopicAdmin
	if cfg.Kafka.TopicAdmin.AutoCreate {
		topicAdmin, err = kafka.NewTopicAdmin(cfg.Kafka.Brokers, &cfg.Kafka.TopicAdmin, log)
		if err != nil {
			log.Fatal("创建 Topic 管理员失败", zap.Error(err))
		}
		defer topicAdmin.Close()

		// 确保所有 topics 存在
		topics := make([]string, 0, len(cfg.Routing.Topics))
		for _, topic := range cfg.Routing.Topics {
			topics = append(topics, topic)
		}
		if err := topicAdmin.EnsureTopics(ctx, topics); err != nil {
			log.Warn("确保 Topics 存在失败",
				zap.Strings("主题列表", topics),
				zap.Error(err))
		} else {
			log.Info("所有 Topics 已确保存在", zap.Strings("主题列表", topics))
		}
	}

	// 创建 Kafka Producer
	producer, err := kafka.NewProducer(&cfg.Kafka, log)
	if err != nil {
		log.Fatal("创建 Kafka Producer 失败", zap.Error(err))
	}
	defer producer.Close()

	// 第二步：如果配置了 Kafka 日志输出，重新初始化 logger（包含 Kafka core）
	if hasKafkaLog && cfg.Logger.Kafka.Enabled {
		log.Info("重新初始化 logger，启用 Kafka 日志输出",
			zap.String("kafka主题", cfg.Logger.Kafka.Topic))
		if err := logger.Init(&cfg.Logger, producer); err != nil {
			log.Warn("重新初始化 logger 失败，继续使用文件日志", zap.Error(err))
		} else {
			log = logger.GetLogger()
			log.Info("Logger 已重新初始化，Kafka 日志输出已启用")
		}
	}

	// 创建 Kafka Writer
	writer := kafka.NewWriter(producer, r, &cfg.Kafka, log)
	writer.Start(ctx)
	// 注意：writer.Close() 应该在优雅关闭时调用，而不是在 defer 中
	// 因为需要先关闭 channel，然后等待 workers 完成

	// 启动健康检查服务器（启动时不就绪）
	var healthServer *health.Server
	var metricsServer *http.Server
	if cfg.Monitoring.Enabled {
		healthServer = health.NewServer(cfg.Monitoring.HealthPort, eventQueue, log)
		healthServer.SetReady(false) // 初始状态为未就绪
		
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Error("健康检查服务器发生 panic",
						zap.Any("panic", r),
						zap.Stack("stack"))
				}
			}()
			if err := healthServer.Start(); err != nil && err != http.ErrServerClosed {
				log.Error("健康检查服务器错误", zap.Error(err))
			}
		}()

		// 启动 Prometheus metrics 服务器
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.Handler())
		metricsServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.Monitoring.MetricsPort),
			Handler: metricsMux,
		}
		
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Error("指标服务器发生 panic",
						zap.Any("panic", r),
						zap.Stack("stack"))
				}
			}()
			log.Info("正在启动指标服务器", zap.Int("端口", cfg.Monitoring.MetricsPort))
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("指标服务器错误", zap.Error(err))
			}
		}()
		
		// 等待健康检查服务器启动
		time.Sleep(100 * time.Millisecond)
	}

	// 启动 gRPC 事件读取器
	reconnectMgr := grpc.NewReconnectManager(grpcClient, cfg, log)
	grpcEventCh := make(chan *tetragon.GetEventsResponse, cfg.Tetragon.Stream.MaxQueue)
	
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Error("gRPC 重连管理器发生 panic",
					zap.Any("panic", r),
					zap.Stack("stack"))
			}
		}()
		reconnectMgr.RunWithReconnect(ctx, grpcEventCh)
		log.Info("gRPC 重连管理器已停止")
	}()

	// 启动事件处理循环
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Error("事件处理循环发生 panic",
					zap.Any("panic", r),
					zap.Stack("stack"))
			}
		}()
		processEvents(ctx, grpcEventCh, eventQueue, sampler, log)
		log.Info("事件处理循环已停止")
	}()

	// 启动 Kafka 写入循环
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Error("Kafka 写入循环发生 panic",
					zap.Any("panic", r),
					zap.Stack("stack"))
			}
		}()
		writeToKafka(ctx, eventQueue, normalizer, r, writer, &cfg.Routing.PartitionKey, log)
		log.Info("Kafka 写入循环已停止")
	}()

	// 优雅启动：等待所有组件就绪
	log.Info("等待所有组件就绪...")
	time.Sleep(2 * time.Second) // 等待组件初始化
	
	// 标记服务为就绪状态
	if healthServer != nil {
		healthServer.SetReady(true)
		log.Info("服务已就绪，开始接收请求")
	}

	// 等待信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	
	// 停止信号监听，避免重复处理
	signal.Stop(sigChan)

	log.Info("收到关闭信号，开始优雅关闭",
		zap.String("信号", sig.String()),
		zap.Int("队列当前大小", eventQueue.Size()),
		zap.Int("队列容量", eventQueue.Capacity()))

	// 第一步：标记服务为关闭状态，拒绝新的健康检查请求
	if healthServer != nil {
		healthServer.SetShutdown(true)
		log.Info("已标记服务为关闭状态")
	}

	// 第二步：停止接收新事件（取消上下文）
	log.Info("停止接收新事件...")
	cancel()

	// 第三步：等待队列清空和 writeToKafka goroutine 停止（最多 30 秒）
	// 注意：由于 cancel() 已经调用，writeToKafka 会在处理完当前事件后退出
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	log.Info("等待队列清空和写入循环停止...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	lastQueueSize := eventQueue.Size()
	queueDrained := false

	for !queueDrained {
		select {
		case <-shutdownCtx.Done():
			log.Warn("关闭超时，强制退出",
				zap.Int("剩余队列大小", eventQueue.Size()))
			queueDrained = true
		case <-ticker.C:
			queueSize := eventQueue.Size()
			if queueSize == 0 {
				// 队列已清空，再等待一小段时间确保 writeToKafka 已处理完
				time.Sleep(500 * time.Millisecond)
				if eventQueue.Size() == 0 {
					log.Info("队列已清空，写入循环应已停止")
					queueDrained = true
				}
			} else {
				// 队列大小发生变化时记录
				if queueSize != lastQueueSize {
					log.Info("等待队列清空中...",
						zap.Int("剩余队列大小", queueSize),
						zap.Int("队列容量", eventQueue.Capacity()))
					lastQueueSize = queueSize
				}
			}
		}
	}

	// 第四步：关闭 Writer channel，然后等待所有 Kafka 写入任务完成
	// 注意：此时 writeToKafka goroutine 应该已经因为 context 取消而退出
	// 但可能还有消息在 writer 的内部队列中
	log.Info("关闭 Writer channel...")
	writer.Close() // 关闭 channel，停止接收新消息
	
	log.Info("等待所有 Kafka 写入任务完成...")
	writer.Wait()
	log.Info("所有 Kafka 写入任务已完成")

	// 第五步：关闭 HTTP 服务器
	if cfg.Monitoring.Enabled {
		shutdownHTTPCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer httpCancel()

		if healthServer != nil {
			log.Info("正在关闭健康检查服务器...")
			if err := healthServer.Shutdown(shutdownHTTPCtx); err != nil {
				log.Warn("关闭健康检查服务器超时", zap.Error(err))
			}
		}

		if metricsServer != nil {
			log.Info("正在关闭指标服务器...")
			if err := metricsServer.Shutdown(shutdownHTTPCtx); err != nil {
				log.Warn("关闭指标服务器超时", zap.Error(err))
			}
		}
	}

	// 第六步：等待所有 goroutine 退出
	log.Info("等待所有 goroutine 退出...")
	goroutineDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(goroutineDone)
	}()

	select {
	case <-goroutineDone:
		log.Info("所有 goroutine 已退出")
	case <-time.After(10 * time.Second):
		log.Warn("等待 goroutine 退出超时",
			zap.Int("超时时间", 10))
	}

	// 第七步：同步日志（确保所有日志都发送完成，包括 Kafka 日志）
	// 注意：必须在关闭 producer 之前执行，因为 Kafka 日志需要 producer
	// 优雅关闭过程中的所有日志都应该能够发送到 Kafka
	log.Info("正在同步日志...")
	if err := logger.Sync(); err != nil {
		log.Warn("同步日志失败", zap.Error(err))
	} else {
		log.Info("日志同步完成")
	}
	
	log.Info("优雅关闭完成，程序退出")
}

// getEventNodeName 获取事件节点名（辅助函数）
func getEventNodeName(event *tetragon.GetEventsResponse) string {
	nodeName := event.GetNodeName()
	if nodeName != "" {
		return nodeName
	}
	return "unknown"
}

// processEvents 处理 gRPC 事件
func processEvents(ctx context.Context, grpcEventCh <-chan *tetragon.GetEventsResponse, 
	eventQueue *queue.Queue, sampler *queue.Sampler, log *zap.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-grpcEventCh:
			// 采样
			if !sampler.ShouldSample(event) {
				continue
			}

			// 优化：只调用一次 DetectEventType，避免重复调用
			eventType := router.DetectEventType(event)

			// 入队
			if err := eventQueue.Push(ctx, event); err != nil {
				if err == queue.ErrQueueFull {
					// 优化：队列满时使用 Warn 级别，因为这是需要关注的问题
					log.Warn("队列已满，丢弃事件",
						zap.String("事件类型", eventType),
						zap.Int("队列当前大小", eventQueue.Size()),
						zap.Int("队列容量", eventQueue.Capacity()))
				} else {
					log.Error("推送事件到队列失败",
						zap.String("事件类型", eventType),
						zap.Error(err))
				}
			}

			// 更新指标（使用缓存的 eventType）
			metrics.EventsInTotal.WithLabelValues(eventType).Inc()
		}
	}
}

// writeToKafka 写入 Kafka
// 性能优化：移除 default case，避免忙等待，使用阻塞的 Pop
func writeToKafka(ctx context.Context, eventQueue *queue.Queue, normalizer *normalize.EventNormalizer,
	r *router.Router, writer *kafka.Writer, partitionKeyCfg *config.PartitionKeyConfig, log *zap.Logger) {
	for {
		event, err := eventQueue.Pop(ctx)
		if err != nil {
			if err == context.Canceled {
				return
			}
			log.Error("从队列弹出事件失败", zap.Error(err))
			continue
		}

		// 记录处理开始时间（用于计算总处理延迟）
		processingStart := time.Now()

		// 规范化事件
		normalizeStart := time.Now()
		eventType := router.DetectEventType(event)
		schema, err := normalizer.Normalize(event)
		if err != nil {
			metrics.NormalizeErrorsTotal.WithLabelValues(eventType).Inc()
			log.Error("规范化事件失败",
				zap.String("事件类型", eventType),
				zap.String("节点名", getEventNodeName(event)),
				zap.Error(err))
			continue
		}
		// 获取 trace ID，用于日志追踪
		traceID := schema.TraceID
		normalizeLatency := time.Since(normalizeStart).Milliseconds()
		metrics.NormalizeLatencyMs.WithLabelValues(schema.Type).Observe(float64(normalizeLatency))
		metrics.EventProcessingLatencyMs.WithLabelValues("normalize").Observe(float64(normalizeLatency))

		// 序列化为 JSON
		serializeStart := time.Now()
		value, err := schema.ToJSON()
		if err != nil {
			metrics.NormalizeErrorsTotal.WithLabelValues(schema.Type).Inc()
			log.Error("序列化事件失败",
				zap.String("事件类型", schema.Type),
				zap.String("节点名", schema.Node),
				zap.String("trace_id", traceID),
				zap.Error(err))
			continue
		}
		serializeLatency := time.Since(serializeStart).Milliseconds()
		metrics.EventProcessingLatencyMs.WithLabelValues("serialize").Observe(float64(serializeLatency))

		// 路由到 Topic
		routeStart := time.Now()
		topic := r.Route(event)
		routeLatency := time.Since(routeStart).Milliseconds()
		metrics.EventProcessingLatencyMs.WithLabelValues("route").Observe(float64(routeLatency))

		// 生成去重 Key（方案 1：Kafka Compacted Topic）
		key := kafka.GenerateDedupKey(event, partitionKeyCfg)

		// 写入 Kafka
		msg := &kafka.Message{
			Event:   event,
			Topic:   topic,
			Key:     key,
			Value:   value,
			TraceID: traceID, // 传递 trace ID 到 Message，用于 Writer 中的日志
		}

		if err := writer.Write(msg); err != nil {
			metrics.EventsOutTotal.WithLabelValues(topic, "failed").Inc()
			log.Error("写入 Kafka 失败", 
				zap.String("主题", topic),
				zap.String("消息键", key),
				zap.Int("消息大小", len(value)),
				zap.String("事件类型", schema.Type),
				zap.String("trace_id", traceID),
				zap.Error(err))
		} else {
			metrics.EventsOutTotal.WithLabelValues(topic, "success").Inc()
			// 成功写入时使用 Debug 级别记录 trace ID（可选）
			log.Debug("事件已写入 Kafka",
				zap.String("主题", topic),
				zap.String("trace_id", traceID))
		}

		// 记录总处理延迟
		totalLatency := time.Since(processingStart).Milliseconds()
		metrics.EventProcessingLatencyMs.WithLabelValues("total").Observe(float64(totalLatency))
	}
}
