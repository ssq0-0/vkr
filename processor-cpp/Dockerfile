# Build stage
FROM ubuntu:22.04 AS builder

# Устанавливаем зависимости
RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    git \
    pkg-config \
    libgrpc++-dev \
    libprotobuf-dev \
    protobuf-compiler-grpc \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Копируем protobuf файлы
COPY proto/ /app/proto/

# Копируем исходники C++
COPY processor-cpp/ /app/processor-cpp/

WORKDIR /app/processor-cpp

# Создаем директорию для сборки
RUN mkdir -p build && cd build && \
    cmake -DCMAKE_BUILD_TYPE=Release .. && \
    make -j$(nproc)

# Runtime stage
FROM ubuntu:22.04 AS runtime

RUN apt-get update && apt-get install -y \
    libgrpc++1 \
    libprotobuf23 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Копируем собранный бинарник
COPY --from=builder /app/processor-cpp/build/processor_server /app/

# Копируем бенчмарк
COPY --from=builder /app/processor-cpp/build/arena_benchmark /app/

EXPOSE 9090 9091

ENV HOST=0.0.0.0
ENV PORT=9090
ENV METRICS_PORT=9091

ENTRYPOINT ["/app/processor_server"]
CMD ["--host", "0.0.0.0", "--port", "9090"]


