# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Копируем go.mod и go.sum
COPY gateway/go.mod gateway/go.sum ./

# Скачиваем зависимости
RUN go mod download

# Копируем исходный код
COPY gateway/ ./

# Собираем бинарник
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o gateway ./cmd/server

# Runtime stage
FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /app

# Копируем бинарник
COPY --from=builder /app/gateway .

EXPOSE 8080

ENV HTTP_PORT=8080
ENV PROCESSOR_ADDRS=processor:9090

ENTRYPOINT ["./gateway"]


