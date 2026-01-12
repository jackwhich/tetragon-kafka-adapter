# 多阶段构建
FROM golang:1.21-alpine AS builder

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

# 复制配置文件
COPY configs/config.yaml /app/config.yaml

EXPOSE 8080 9090

ENTRYPOINT ["./tetragon-kafka-adapter"]
CMD ["-config", "/app/config.yaml"]
