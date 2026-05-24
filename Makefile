.PHONY: all build clean proto docker-build docker-up docker-down test benchmark help

# Цвета для вывода
GREEN := \033[0;32m
YELLOW := \033[0;33m
NC := \033[0m # No Color

all: help

help:
	@echo "$(GREEN)Streaming Processor System$(NC)"
	@echo ""
	@echo "$(YELLOW)Build commands:$(NC)"
	@echo "  make proto       - Generate protobuf files"
	@echo "  make build       - Build all services locally"
	@echo "  make build-cpp   - Build C++ processor"
	@echo "  make build-go    - Build Go services"
	@echo ""
	@echo "$(YELLOW)Docker commands:$(NC)"
	@echo "  make docker-build   - Build all Docker images"
	@echo "  make docker-up      - Start all services"
	@echo "  make docker-down    - Stop all services"
	@echo "  make docker-logs    - View service logs"
	@echo ""
	@echo "$(YELLOW)Test commands:$(NC)"
	@echo "  make test           - Run unit tests"
	@echo "  make test-cpp       - Run C++ tests"
	@echo "  make benchmark      - Run benchmark"
	@echo "  make load-test      - Run load test (TOKEN=<jwt> MEMORY_MODE=arena|no_arena)"
	@echo ""
	@echo "$(YELLOW)Other:$(NC)"
	@echo "  make clean          - Clean build artifacts"

# Генерация protobuf
proto:
	@echo "$(GREEN)Generating protobuf files...$(NC)"
	protoc --go_out=gateway/proto --go_opt=paths=source_relative \
		--go-grpc_out=gateway/proto --go-grpc_opt=paths=source_relative \
		-I proto proto/streaming.proto
	@echo "Protobuf files generated"

# Сборка C++ процессора
build-cpp:
	@echo "$(GREEN)Building C++ processor...$(NC)"
	mkdir -p processor-cpp/build
	cd processor-cpp/build && cmake -DCMAKE_BUILD_TYPE=Release .. && make -j$$(nproc)
	@echo "C++ processor built"

# Сборка Go сервисов
build-go:
	@echo "$(GREEN)Building Go services...$(NC)"
	cd gateway && go build -o bin/gateway ./cmd/server
	@echo "Go services built"

# Полная сборка
build: build-cpp build-go
	@echo "$(GREEN)All services built$(NC)"

# Docker сборка
docker-build:
	@echo "$(GREEN)Building Docker images...$(NC)"
	docker-compose build

# Запуск в Docker
docker-up:
	@echo "$(GREEN)Starting services...$(NC)"
	docker-compose up -d
	@echo ""
	@echo "Services started:"
	@echo "  - Gateway:        http://localhost:8080"
	@echo "  - Processor C++:  localhost:9090 (gRPC)"
	@echo "  - PostgreSQL:      localhost:5432"
	@echo "  - Prometheus:     http://localhost:9099"
	@echo "  - Grafana:        http://localhost:3000 (admin/admin)"

docker-down:
	@echo "$(GREEN)Stopping services...$(NC)"
	docker-compose down

docker-logs:
	docker-compose logs -f

# Запуск с C++ процессором
docker-up-cpp:
	@echo "$(GREEN)Starting services with C++ processor...$(NC)"
	PROCESSOR_ADDRS=processor-cpp:9090 docker-compose up -d gateway processor-cpp postgres prometheus grafana

# Тесты C++
test-cpp:
	@echo "$(GREEN)Running C++ tests...$(NC)"
	cd processor-cpp/build && ctest --output-on-failure

# Тесты Go
test-go:
	@echo "$(GREEN)Running Go tests...$(NC)"
	cd gateway && go test ./...
	cd tests && go test ./...

test: test-cpp test-go

# Бенчмарк
benchmark:
	@echo "$(GREEN)Running benchmark...$(NC)"
	cd tests/benchmarks && go run benchmark.go \
		-url=http://localhost:8080/api/process \
		-token="$(TOKEN)" \
		-requests=1000 \
		-concurrency=10

# Нагрузочный тест
load-test:
	@echo "$(GREEN)Running load test...$(NC)"
	cd tests/load && go run load_test.go \
		-url=http://localhost:8080/api/process \
		-token="$(TOKEN)" \
		-memory-mode="$${MEMORY_MODE:-arena}" \
		-c=50 \
		-d=60s \
		-rps=500

# Бенчмарк Memory Arena
arena-benchmark:
	@echo "$(GREEN)Running Memory Arena benchmark...$(NC)"
	./processor-cpp/build/arena_benchmark

# Очистка
clean:
	@echo "$(GREEN)Cleaning...$(NC)"
	rm -rf processor-cpp/build
	rm -f gateway/bin/*
	rm -f processor-go/bin/*
	docker-compose down -v --rmi local 2>/dev/null || true
	@echo "Clean complete"

# Быстрый запуск для разработки
dev: docker-build docker-up
	@echo "$(GREEN)Development environment ready$(NC)"
	@echo "Open http://localhost:8080 in your browser"


