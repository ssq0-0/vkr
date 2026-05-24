# Grafana — запросы для дашборда (Prometheus)

Источник: Prometheus http://localhost:9099  
Grafana: http://localhost:3000 (admin / admin)

Перед прогоном: Status → Targets — job `gateway` и `processor-cpp` в состоянии **UP**.

Вводите запросы **только латиницей** (без «ё» в конце имён метрик).

---

## Ряд 1 — Нагрузка (Gateway)

ВАЖНО: в Gateway метка `endpoint` — это **внутреннее имя**, не URL пути.
Для HTTP `/api/process` в Prometheus будет **`endpoint="process_frame"`**.

Проверка в Explore (должны появиться series):
```promql
gateway_requests_total
```

### 1. RPS (обработка кадра /api/process)
```promql
sum(rate(gateway_requests_total{endpoint="process_frame"}[1m]))
```
Legend: `RPS process`

### 2. Ошибки Gateway (все типы)
```promql
sum(rate(gateway_errors_total[1m])) by (type)
```

### 3. Обработано кадров/блоков (накопительно, прирост)
```promql
sum(rate(gateway_frames_processed_total[1m]))
```

---

## Ряд 2 — Задержка (Gateway, сквозная)

### 4. Средняя задержка process_frame (с)
```promql
sum(rate(gateway_request_latency_seconds_sum{endpoint="process_frame"}[1m]))
/
sum(rate(gateway_request_latency_seconds_count{endpoint="process_frame"}[1m]))
```
Unit: seconds (s)

Если «No data» — за последний час не было запросов: на панели **Run query** после нагрузки или смените диапазон на **Last 5 minutes**, либо запрос без rate:
```promql
gateway_request_latency_seconds_sum{endpoint="process_frame"}
/
gateway_request_latency_seconds_count{endpoint="process_frame"}
```

### 5. P95 задержка process_frame
```promql
histogram_quantile(0.95,
  sum(rate(gateway_request_latency_seconds_bucket{endpoint="process_frame"}[1m])) by (le)
)
```

### 6. P99 задержка process_frame
```promql
histogram_quantile(0.99,
  sum(rate(gateway_request_latency_seconds_bucket{endpoint="process_frame"}[1m])) by (le)
)
```

---

## Ряд 3 — C++ Processor (производительность)

### 7. Блоков в секунду (processor)
```promql
rate(processor_cpp_frames_processed_total[1m])
```

### 8. Среднее время обработки в C++ (мс)
```promql
processor_cpp_avg_processing_time_ms
```

### 9. FPS (gauge)
```promql
processor_cpp_frames_per_second
```

---

## Ряд 4 — Memory Arena (режим arena)

### 10. Текущий размер арены (байты)
```promql
processor_cpp_current_arena_size_bytes
```

### 11. Пик арены (байты)
```promql
processor_cpp_peak_arena_size_bytes
```

### 12. Активные арены (после нагрузки → 0)
```promql
processor_cpp_active_arenas
```

### 13. Сбросы арены (прирост)
```promql
rate(processor_cpp_total_arena_resets[1m])
```

### 14. Кадры в режиме arena (прирост)
```promql
rate(processor_cpp_arena_mode_frames_total[1m])
```

---

## Ряд 5 — Heap / No Arena

### 15. Heap-аллокации (счётчик, прирост)
```promql
rate(processor_cpp_heap_allocations_total[1m])
```

### 16. Heap байты (суммарный счётчик аллокаций, прирост)
```promql
rate(processor_cpp_heap_bytes_allocated_total[1m])
```

### 17. Кадры в режиме no_arena (прирост)
```promql
rate(processor_cpp_heap_mode_frames_total[1m])
```

---

## Ряд 6 — WebSocket (опционально)

### 18. Активные WS-соединения
```promql
gateway_active_connections
```

---

## Как собрать дашборд в UI

1. Explore → вставить запрос → Run query → убедиться, что график есть.
2. Над графиком: **Add to dashboard** → Create new dashboard → имя: `Diplom Arena vs No Arena`.
3. Повторить для каждого запроса (новая панель).
4. Сохранить дашборд.

Для сравнения двух прогонов: делайте скрин **во время** прогона Arena, затем отдельный прогон No Arena (или две вкладки с разным временным диапазоном).

---

## Проверка, что метрика существует

В Explore → Metrics browser или запрос:
```promql
{__name__=~"processor_cpp_.*"}
```
```promql
{__name__=~"gateway_.*"}
```
