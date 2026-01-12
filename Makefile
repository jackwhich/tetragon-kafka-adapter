.PHONY: build build-linux run test clean docker-build

# 本地编译（当前平台）
build:
	@mkdir -p bin
	go build -o bin/tetragon-kafka-adapter ./cmd/consumer

# Linux 交叉编译（用于在 macOS 上编译 Linux 二进制）
build-linux:
	@mkdir -p bin
	GOOS=linux GOARCH=amd64 go build -o bin/tetragon-kafka-adapter-linux-amd64 ./cmd/consumer
	@echo "Linux 二进制文件已生成: bin/tetragon-kafka-adapter-linux-amd64"

# 运行
run:
	go run ./cmd/consumer/main.go

# 测试
test:
	go test ./...

# 清理
clean:
	rm -rf bin/
	go clean

# Docker 构建
docker-build:
	docker build -t tetragon-kafka-adapter:latest .
