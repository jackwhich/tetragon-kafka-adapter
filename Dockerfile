# 多阶段构建
FROM golang:1.25-alpine AS builder

WORKDIR /build

# 复制 go mod 文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o tetragon-kafka-adapter ./cmd/consumer

# 运行阶段
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /build/tetragon-kafka-adapter .

EXPOSE 8080 9090

# 注意：配置文件通过 ConfigMap 挂载到 /etc/tetragon-kafka-adapter/config.yaml
ENTRYPOINT ["./tetragon-kafka-adapter"]
CMD ["-config", "/etc/tetragon-kafka-adapter/config.yaml"]
