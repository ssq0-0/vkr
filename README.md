# 🎬 Streaming Media Processor

Система потоковой обработки мультимедийных данных с использованием gRPC bidirectional streaming. Дипломная версия демонстрирует законченную ИС: web-доступ с авторизацией, PostgreSQL-журналирование пользовательских запусков, мониторинг и C++ обработчик с переключаемыми режимами Memory Arena / No Arena.

## 📋 Содержание

- [Архитектура](#архитектура)
- [Структура проекта](#структура-проекта)
- [Быстрый старт](#быстрый-старт)
- [API](#api)
- [Memory Arena](#memory-arena)
- [Сравнение производительности](#сравнение-производительности)
- [Мониторинг](#мониторинг)
- [Тестирование](#тестирование)

## 🏗 Архитектура

```
┌─────────────────┐     HTTP/WS      ┌─────────────────┐     gRPC       ┌─────────────────┐
│                 │ ────────────────► │                 │ ─────────────► │                 │
│    Клиенты      │                  │    Gateway      │                │   Processor     │
│  (Web/Mobile)   │ ◄──────────────── │      Gateway    │ ◄───────────── │   Processor C++ │
│                 │                  │                 │                │                 │
└─────────────────┘                  └─────────────────┘                └─────────────────┘
                                            │
                                            ▼
                                    ┌─────────────────┐
                                    │   Prometheus    │
                                    │   + Grafana     │
                                    └─────────────────┘
```

### Компоненты

1. **Gateway Service (Go)** - порт 8080
   - HTTP API для приема кадров
   - Регистрация, вход и проверка Bearer-токена
   - Запись истории обработки в PostgreSQL
   - WebSocket для real-time стриминга
   - Преобразование JSON → Protobuf
   - Пул gRPC соединений
   - Rate limiting и метрики

2. **Processor Service (C++)** - порт 9090
   - gRPC сервер с bidirectional streaming
   - **Memory Arena** для эффективного управления памятью
   - SIMD-оптимизированная обработка изображений
   - Gaussian blur, sharpen, edge detection, grayscale

3. **PostgreSQL** - порт 5432
   - Пользователи и сессии авторизации
   - История запусков обработки
   - Проектная таблица результатов экспериментов

4. **Processor Service (C++)** - порт 9090
   - Режим `arena`: временные буферы выделяются из Memory Arena
   - Режим `no_arena`: тот же алгоритм работает через обычные heap-выделения
   - Режим выбирается параметром `memory_mode` в HTTP-запросе и передаётся в gRPC metadata `memory-mode`

## 📁 Структура проекта

```
streaming-system/
├── proto/                          # Protobuf схемы
│   └── streaming.proto
├── gateway/                        # Gateway Service (Go)
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── http/handlers.go
│   │   ├── grpc/client.go
│   │   └── metrics/metrics.go
│   └── Dockerfile
├── processor-cpp/                  # Processor Service (C++)
│   ├── src/
│   │   ├── arena/                 # Memory Arena реализация
│   │   │   ├── memory_arena.hpp
│   │   │   └── memory_arena.cpp
│   │   ├── processing/            # Обработка кадров
│   │   │   ├── image_processor.hpp
│   │   │   └── image_processor.cpp
│   │   └── server/                # gRPC сервер
│   │       ├── grpc_server.hpp
│   │       └── grpc_server.cpp
│   ├── tests/
│   ├── CMakeLists.txt
│   └── Dockerfile
├── db/                             # SQL-схема PostgreSQL
├── tests/                          # Тесты и бенчмарки
│   ├── load/load_test.go
│   └── benchmarks/benchmark.go
├── monitoring/                     # Конфигурации мониторинга
├── docker-compose.yml
├── Makefile
└── README.md
```

## 🚀 Быстрый старт

### Требования

- Docker и Docker Compose
- Go 1.21+ (для локальной разработки)
- CMake 3.16+ и компилятор C++17 (для локальной сборки C++)
- protoc (для генерации proto файлов)

### Запуск через Docker

```bash
# Клонирование и запуск
cd streaming-system

# Сборка и запуск всех сервисов
make docker-build
make docker-up

# Или одной командой
make dev
```

После запуска:
- **Gateway UI**: http://localhost:8080
- **PostgreSQL**: localhost:5432 (streaming/streaming)
- **Prometheus**: http://localhost:9099
- **Grafana**: http://localhost:3000 (admin/admin)

### Локальная сборка

```bash
# Сборка C++ процессора
make build-cpp

# Сборка Go сервисов
make build-go

# Полная сборка
make build
```

## 📡 API

### HTTP Endpoints

#### POST /api/process
Обработка одного кадра.

**Request:**
```json
{
  "data": "<base64 encoded RGB data>",
  "width": 640,
  "height": 480,
  "filter": "gaussian_blur",
  "blur_radius": 5.0,
  "memory_mode": "arena"
}
```

**Response:**
```json
{
  "data": "<base64 encoded result>",
  "success": true,
  "processing_time_ms": 2.45,
  "memory_mode": "arena"
}
```

Перед вызовом `/api/process` нужно получить токен через `/api/register` или `/api/login` и передать заголовок `Authorization: Bearer <token>`.

#### POST /api/register
Регистрация пользователя.

#### POST /api/login
Вход пользователя и получение токена.

#### GET /api/history
История запусков текущего пользователя.

#### POST /api/stream
Потоковая обработка (raw binary).

Query parameters:
- `width` - ширина кадра (default: 640)
- `height` - высота кадра (default: 480)
- `filter` - тип фильтра
- `blur_radius` - радиус размытия

#### WS /ws
WebSocket для real-time стриминга.

Формат сообщений - JSON аналогично `/api/process`.

#### GET /health
Проверка здоровья сервиса.

#### GET /stats
Статистика процессора.

#### GET /metrics
Prometheus метрики.

### Поддерживаемые фильтры

| Фильтр | Описание |
|--------|----------|
| `gaussian_blur` | Размытие по Гауссу |
| `sharpen` | Повышение резкости |
| `edge_detect` | Детекция границ (Sobel) |
| `grayscale` | Конвертация в ч/б |

## 🧠 Memory Arena

### Что это?

Memory Arena - техника управления памятью, при которой память выделяется большими блоками и освобождается целиком, минуя отдельные вызовы `free()` для каждого объекта.

### Преимущества

1. **Быстрое выделение** - просто смещение указателя
2. **Быстрое освобождение** - сброс указателя на начало блока
3. **Нет фрагментации** - память выделяется последовательно
4. **Лучшая cache locality** - данные рядом в памяти
5. **Предсказуемое поведение** - нет GC pauses

### Реализация

```cpp
class MemoryArena {
public:
    // Выделение памяти
    void* allocate(size_t size, size_t alignment = 16);
    
    // Создание объекта
    template<typename T, typename... Args>
    T* create(Args&&... args);
    
    // Выделение массива
    template<typename T>
    T* allocate_array(size_t count);
    
    // Сброс арены (освобождение всей памяти)
    void reset();
    
    // Статистика
    size_t allocated_bytes() const;
    size_t used_bytes() const;
    size_t total_allocations() const;
};
```

### Пример использования

```cpp
// Создаем арену на 8 MB
MemoryArena arena(8 * 1024 * 1024);

// Обрабатываем кадры
for (int i = 0; i < 1000; ++i) {
    // Выделяем память из арены
    auto* frame = arena.allocate_array<uint8_t>(640 * 480 * 3);
    auto* temp = arena.allocate_array<float>(640 * 480 * 3);
    
    // Обрабатываем...
    process_frame(frame, temp);
    
    // Каждые 100 кадров сбрасываем арену
    if ((i + 1) % 100 == 0) {
        arena.reset();  // Мгновенное освобождение!
    }
}
```

### Сравнение с режимом No Arena

| Аспект | C++ Memory Arena | C++ No Arena |
|--------|------------------|--------------|
| Аллокация | O(1), смещение указателя | Обычные heap-выделения контейнеров |
| Освобождение | O(1), сброс указателя | Освобождение при разрушении объектов |
| Фрагментация | Ниже для временных буферов | Возможна |
| Чистота эксперимента | Меняется только стратегия памяти | Алгоритм и язык остаются теми же |
| Сложность | Выше | Ниже |

## 📊 Сравнение производительности

### Запуск бенчмарка

```bash
# Запустить сервисы
make docker-up

# Запустить бенчмарк
make benchmark
```

### Ожидаемые результаты

| Метрика | C++ (Arena) | C++ (No Arena) | Разница |
|---------|-------------|----------------|---------|
| RPS | зависит от стенда | зависит от стенда | определяется экспериментом |
| P50 Latency | измеряется | измеряется | определяется экспериментом |
| P99 Latency | измеряется | измеряется | определяется экспериментом |
| Memory allocations | arena counters | heap counters | определяется экспериментом |

*Результаты зависят от железа и нагрузки*

### Нагрузочное тестирование

```bash
# 500 RPS в течение 60 секунд
make load-test

# Кастомные параметры
cd tests/load && go run load_test.go \
    -url=http://localhost:8080/api/process \
    -c=100 \
    -d=120s \
    -rps=1000
```

## 📈 Мониторинг

### Prometheus метрики

**Gateway:**
- `gateway_requests_total` - количество запросов
- `gateway_errors_total` - количество ошибок
- `gateway_frames_processed_total` - обработано кадров
- `gateway_request_latency_seconds` - latency запросов

**Processor C++:**
- Метрики арены (выделено, использовано, сбросов)
- Метрики heap-режима (`processor_cpp_heap_allocations_total`, `processor_cpp_heap_bytes_allocated_total`)
- Счётчики кадров по режимам (`processor_cpp_arena_mode_frames_total`, `processor_cpp_heap_mode_frames_total`)
- Время обработки кадров

### Grafana дашборды

1. Откройте http://localhost:3000
2. Логин: admin / admin
3. Добавьте Prometheus datasource (http://prometheus:9090)
4. Создайте дашборд с метриками

## 🧪 Тестирование

### Unit тесты

```bash
# Все тесты
make test

# Только C++
make test-cpp

# Только Gateway и тестовые утилиты на Go
make test-go
```

### Memory Arena бенчмарк

```bash
make arena-benchmark
```

Пример вывода:
```
=== Memory Arena Benchmark ===
Frame size: 640x480x3 = 900 KB

Benchmark                              Avg Time      Throughput   Allocations   Peak Mem
------------------------------------------------------------------------------------------
Arena + Gaussian Blur                  1.45 ms       689.7 fps   3000 allocs   8 MB
Malloc + Simple Copy                   0.12 ms      8333.3 fps   3000 allocs   0 MB
Arena Allocations Only                 0.02 ms     50000.0 fps   3000 allocs   8 MB
Malloc/Free Only                       0.08 ms     12500.0 fps   3000 allocs   0 MB
```

## 🔧 Конфигурация

### Переменные окружения

**Gateway:**
- `HTTP_PORT` - HTTP порт (default: 8080)
- `PROCESSOR_ADDRS` - адреса процессоров через запятую
- `DATABASE_URL` - строка подключения PostgreSQL
- `AUTH_SECRET` - секрет подписи токенов

**Processor C++:**
- `HOST` - хост для прослушивания (default: 0.0.0.0)
- `PORT` - gRPC порт (default: 9090)
- `METRICS_PORT` - порт метрик (default: 9091)

## 📚 Полезные команды

```bash
# Просмотр логов
make docker-logs

# Остановка сервисов
make docker-down

# Очистка всего
make clean

# Справка
make help
```

## 🎓 Выводы для отчета

### Когда Memory Arena оправдана:

1. **Пакетная обработка** - много объектов создаются и освобождаются вместе
2. **Реал-тайм системы** - недопустимы GC паузы
3. **Высокая частота аллокаций** - тысячи объектов в секунду
4. **Предсказуемый размер данных** - известно заранее сколько памяти нужно

### Когда НЕ оправдана:

1. **Простые приложения** - overhead на реализацию не окупится
2. **Долгоживущие объекты** - арена не освобождает их отдельно
3. **Непредсказуемый размер** - может не хватить места в блоке
4. **Команда без опыта C++** - легко допустить ошибки

### Ключевые метрики для сравнения:

1. Количество системных аллокаций (malloc/free)
2. Latency P99 под нагрузкой
3. Стабильность latency (jitter)
4. Использование памяти (peak vs average)
5. CPU utilization

---

**Автор:** НИР 7 семестр  
**Технологии:** Go, C++17, gRPC, Protobuf, Docker, Prometheus


