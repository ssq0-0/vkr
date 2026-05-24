#!/bin/bash

# Скрипт для генерации Go и C++ файлов из Protobuf

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
PROTO_DIR="$PROJECT_DIR/proto"

echo "=== Generating Protobuf files ==="
echo "Project dir: $PROJECT_DIR"
echo "Proto dir: $PROTO_DIR"

# Генерация для Go (Gateway)
echo "Generating Go files for Gateway..."
mkdir -p "$PROJECT_DIR/gateway/proto"
protoc \
    --go_out="$PROJECT_DIR/gateway/proto" \
    --go_opt=paths=source_relative \
    --go-grpc_out="$PROJECT_DIR/gateway/proto" \
    --go-grpc_opt=paths=source_relative \
    -I "$PROTO_DIR" \
    "$PROTO_DIR/streaming.proto"

# Генерация для Go (Processor)
echo "Generating Go files for Processor Go..."
mkdir -p "$PROJECT_DIR/processor-go/proto"
protoc \
    --go_out="$PROJECT_DIR/processor-go/proto" \
    --go_opt=paths=source_relative \
    --go-grpc_out="$PROJECT_DIR/processor-go/proto" \
    --go-grpc_opt=paths=source_relative \
    -I "$PROTO_DIR" \
    "$PROTO_DIR/streaming.proto"

# Генерация для C++
echo "Generating C++ files for Processor C++..."
mkdir -p "$PROJECT_DIR/processor-cpp/build/generated"
protoc \
    --cpp_out="$PROJECT_DIR/processor-cpp/build/generated" \
    --grpc_out="$PROJECT_DIR/processor-cpp/build/generated" \
    --plugin=protoc-gen-grpc=$(which grpc_cpp_plugin) \
    -I "$PROTO_DIR" \
    "$PROTO_DIR/streaming.proto"

echo "=== Done! ==="
echo "Generated files:"
ls -la "$PROJECT_DIR/gateway/proto/"*.go 2>/dev/null || echo "  No Go files in gateway/proto"
ls -la "$PROJECT_DIR/processor-go/proto/"*.go 2>/dev/null || echo "  No Go files in processor-go/proto"
ls -la "$PROJECT_DIR/processor-cpp/build/generated/"*.cc 2>/dev/null || echo "  No C++ files generated"


