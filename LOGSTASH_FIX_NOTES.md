# Logstash 配置修复说明

## 问题分析

根据 Kibana 仪表盘显示的问题，发现了以下异常：

### 1. 事件数量异常
- **问题**：15 分钟内产生了 641,206 条事件，数量巨大
- **原因**：所有事件都是 `__sk_free` 函数调用，这是一个非常频繁的内核函数

### 2. 进程信息异常
- **问题**：`process.pid` 显示为异常大的值 `1,907,443,407`，且所有事件都是同一个值
- **问题**：`process.binary`、`process.args`、`process.ppid` 都显示为空（`-`）
- **原因**：Logstash 配置没有正确提取嵌套的 JSON 字段

### 3. 规则配置问题
- **问题**：`__sk_free` 事件不应该被记录（根据 `tcp-listen-monitoring.yaml` 规则，应该使用 `action: "NoPost"`）
- **可能原因**：
  - 规则配置错误或未正确应用
  - Tetragon 仍然发送了这些事件（可能是 bug 或配置问题）

## 修复方案

### 1. 添加事件过滤
在 Logstash 配置中添加了对 `__sk_free` 事件的过滤，直接丢弃这些不应该被记录的事件：

```ruby
if [extra] and [extra][function_name] == "__sk_free" {
  drop {}
}
```

### 2. 改进字段提取逻辑
使用 Ruby 代码安全地提取嵌套字段，并验证数据有效性：

- **PID 验证**：确保 PID 值在合理范围内（0 < PID < 10,000,000）
- **类型转换**：正确处理数字和字符串类型的 PID
- **错误处理**：如果提取失败，标记错误但不中断处理

### 3. 字段命名规范
使用点号分隔的字段名（如 `process.pid`），便于在 Elasticsearch 中查询和索引。

## 修复后的配置特点

1. **自动过滤无效事件**：过滤掉 `__sk_free` 等不应该被记录的事件
2. **数据验证**：验证 PID 等关键字段的有效性
3. **错误容错**：即使字段提取失败，也不会中断整个事件处理流程
4. **字段标准化**：统一使用点号分隔的字段名，便于查询

## 建议的后续操作

### 1. 检查 Tetragon 规则
确认 `tcp-listen-monitoring.yaml` 规则是否正确应用：

```bash
kubectl get tracingpolicy tcp-listen-monitoring -n falco -o yaml
```

### 2. 验证规则配置
确认 `__sk_free` 规则中的 `action: "NoPost"` 是否正确：

```yaml
matchActions:
  - action: "UntrackSock"
    argSock: 0
  - action: "NoPost"       # 应该不记录此事件
```

### 3. 重启 Logstash
应用新的配置后，重启 Logstash 使配置生效：

```bash
# 如果使用 systemd
sudo systemctl restart logstash

# 如果使用 Docker
docker restart logstash-container

# 如果使用 Kubernetes
kubectl rollout restart deployment/logstash -n <namespace>
```

### 4. 监控日志
观察 Logstash 日志，确认：
- `__sk_free` 事件是否被正确过滤
- `process.pid` 等字段是否被正确提取
- 是否有 `_process_extraction_error` 标签的事件（表示字段提取失败）

### 5. 验证 Elasticsearch 数据
在 Kibana 中查询，确认：
- 事件数量是否显著减少
- `process.pid` 值是否正常（应该是合理的整数）
- `process.binary` 等字段是否有值

## 注意事项

1. **性能影响**：过滤 `__sk_free` 事件会显著减少事件数量，降低存储和查询压力
2. **数据完整性**：如果确实需要监控套接字释放事件，应该使用更具体的规则，而不是监控所有 `__sk_free` 调用
3. **规则优化**：建议检查所有 Tetragon 规则，确保 `NoPost` 动作正确应用，避免产生不必要的日志

## 相关文件

- **Logstash 配置**：`logstash-k8s-tetragon-policy.conf`
- **Tetragon 规则**：`Tetragon/rules/network/tcp-listen-monitoring.yaml`
- **配置说明**：`LOGSTASH_CONFIG_README.md`
