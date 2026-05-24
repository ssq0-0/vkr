package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ProcessFrameRequest запрос на обработку
type ProcessFrameRequest struct {
	Data       string  `json:"data"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Filter     string  `json:"filter"`
	BlurRadius float32 `json:"blur_radius"`
	MemoryMode string  `json:"memory_mode"`
}

// ProcessFrameResponse ответ
type ProcessFrameResponse struct {
	Success          bool    `json:"success"`
	ProcessingTimeMs float64 `json:"processing_time_ms"`
	Error            string  `json:"error,omitempty"`
}

// Stats статистика нагрузочного теста
type Stats struct {
	totalRequests   int64
	successRequests int64
	failedRequests  int64
	totalLatencyNs  int64
	minLatencyNs    int64
	maxLatencyNs    int64
	latencies       []int64
	latenciesMu     sync.Mutex
}

func (s *Stats) record(latency time.Duration, success bool) {
	ns := latency.Nanoseconds()
	atomic.AddInt64(&s.totalRequests, 1)
	atomic.AddInt64(&s.totalLatencyNs, ns)

	if success {
		atomic.AddInt64(&s.successRequests, 1)
	} else {
		atomic.AddInt64(&s.failedRequests, 1)
	}

	// Обновляем min/max
	for {
		old := atomic.LoadInt64(&s.minLatencyNs)
		if old != 0 && old <= ns {
			break
		}
		if atomic.CompareAndSwapInt64(&s.minLatencyNs, old, ns) {
			break
		}
	}

	for {
		old := atomic.LoadInt64(&s.maxLatencyNs)
		if old >= ns {
			break
		}
		if atomic.CompareAndSwapInt64(&s.maxLatencyNs, old, ns) {
			break
		}
	}

	// Сохраняем для расчета перцентилей
	s.latenciesMu.Lock()
	s.latencies = append(s.latencies, ns)
	s.latenciesMu.Unlock()
}

func (s *Stats) percentile(p float64) time.Duration {
	s.latenciesMu.Lock()
	defer s.latenciesMu.Unlock()

	if len(s.latencies) == 0 {
		return 0
	}

	// Копируем и сортируем
	sorted := make([]int64, len(s.latencies))
	copy(sorted, s.latencies)

	// Простая сортировка пузырьком (для демо)
	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			if sorted[j] > sorted[j+1] {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	idx := int(float64(len(sorted)) * p)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return time.Duration(sorted[idx])
}

func main() {
	url := flag.String("url", "http://localhost:8080/api/process", "Gateway URL")
	concurrency := flag.Int("c", 10, "Number of concurrent workers")
	duration := flag.Duration("d", 30*time.Second, "Test duration")
	rps := flag.Int("rps", 100, "Target requests per second")
	width := flag.Int("width", 640, "Frame width")
	height := flag.Int("height", 480, "Frame height")
	filter := flag.String("filter", "gaussian_blur", "Filter type")
	memoryMode := flag.String("memory-mode", "arena", "Memory mode: arena or no_arena")
	token := flag.String("token", "", "Bearer token for Gateway API")
	flag.Parse()

	log.Printf("=== Load Test Configuration ===")
	log.Printf("URL: %s", *url)
	log.Printf("Concurrency: %d", *concurrency)
	log.Printf("Duration: %s", *duration)
	log.Printf("Target RPS: %d", *rps)
	log.Printf("Frame size: %dx%d", *width, *height)
	log.Printf("Filter: %s", *filter)
	log.Printf("Memory mode: %s", *memoryMode)
	log.Println()

	// Генерируем тестовые данные
	frameSize := *width * *height * 3
	frameData := make([]byte, frameSize)
	rand.Read(frameData)
	encodedData := base64.StdEncoding.EncodeToString(frameData)

	req := ProcessFrameRequest{
		Data:       encodedData,
		Width:      *width,
		Height:     *height,
		Filter:     *filter,
		BlurRadius: 5.0,
		MemoryMode: *memoryMode,
	}
	reqBody, _ := json.Marshal(req)

	stats := &Stats{
		latencies: make([]int64, 0, 100000),
	}

	// Ограничитель скорости
	ticker := time.NewTicker(time.Second / time.Duration(*rps))
	defer ticker.Stop()

	// Канал для остановки
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Запускаем воркеры
	requests := make(chan struct{}, *rps*2)

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			client := &http.Client{
				Timeout: 30 * time.Second,
			}

			for range requests {
				select {
				case <-stop:
					return
				default:
				}

				start := time.Now()
				httpReq, _ := http.NewRequest(http.MethodPost, *url, bytes.NewReader(reqBody))
				httpReq.Header.Set("Content-Type", "application/json")
				if *token != "" {
					httpReq.Header.Set("Authorization", "Bearer "+*token)
				}
				resp, err := client.Do(httpReq)
				latency := time.Since(start)

				success := false
				if err == nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()

					if resp.StatusCode == 200 {
						var result ProcessFrameResponse
						if json.Unmarshal(body, &result) == nil {
							success = result.Success
						}
					}
				}

				stats.record(latency, success)
			}
		}()
	}

	// Генератор запросов
	go func() {
		for {
			select {
			case <-stop:
				close(requests)
				return
			case <-ticker.C:
				select {
				case requests <- struct{}{}:
				default:
					// Очередь переполнена
				}
			}
		}
	}()

	// Прогресс
	progressTicker := time.NewTicker(5 * time.Second)
	defer progressTicker.Stop()

	startTime := time.Now()
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-progressTicker.C:
				elapsed := time.Since(startTime)
				total := atomic.LoadInt64(&stats.totalRequests)
				success := atomic.LoadInt64(&stats.successRequests)
				failed := atomic.LoadInt64(&stats.failedRequests)
				rps := float64(total) / elapsed.Seconds()
				log.Printf("Progress: %d requests (%.1f rps), %d success, %d failed",
					total, rps, success, failed)
			}
		}
	}()

	// Ждем завершения
	time.Sleep(*duration)
	close(stop)
	wg.Wait()

	// Выводим результаты
	elapsed := time.Since(startTime)
	total := atomic.LoadInt64(&stats.totalRequests)
	success := atomic.LoadInt64(&stats.successRequests)
	failed := atomic.LoadInt64(&stats.failedRequests)
	totalLatency := atomic.LoadInt64(&stats.totalLatencyNs)

	log.Println()
	log.Println("=== Results ===")
	log.Printf("Duration: %s", elapsed.Round(time.Millisecond))
	log.Printf("Total requests: %d", total)
	log.Printf("Successful: %d (%.1f%%)", success, float64(success)/float64(total)*100)
	log.Printf("Failed: %d (%.1f%%)", failed, float64(failed)/float64(total)*100)
	log.Printf("RPS: %.1f", float64(total)/elapsed.Seconds())
	log.Println()
	log.Println("Latency:")
	if total > 0 {
		log.Printf("  Min: %s", time.Duration(atomic.LoadInt64(&stats.minLatencyNs)))
		log.Printf("  Avg: %s", time.Duration(totalLatency/total))
		log.Printf("  Max: %s", time.Duration(atomic.LoadInt64(&stats.maxLatencyNs)))
		log.Printf("  P50: %s", stats.percentile(0.50))
		log.Printf("  P95: %s", stats.percentile(0.95))
		log.Printf("  P99: %s", stats.percentile(0.99))
	}
}
