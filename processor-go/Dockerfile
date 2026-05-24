# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Копируем go.mod и go.sum
COPY processor-go/go.mod processor-go/go.sum ./

# Скачиваем зависимости
RUN go mod download

# Копируем исходный код
COPY processor-go/ ./

# Собираем бинарник
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o processor ./cmd/server

# Runtime stage
FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /app

# Копируем бинарник
COPY --from=builder /app/processor .

EXPOSE 9090 9091

ENV PORT=9090
ENV METRICS_PORT=9091

ENTRYPOINT ["./processor"]


