package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"time"
)

// BenchmarkResult результат бенчмарка
type BenchmarkResult struct {
	Name            string
	TotalRequests   int
	Duration        time.Duration
	RPS             float64
	AvgLatency      time.Duration
	MinLatency      time.Duration
	MaxLatency      time.Duration
	P50Latency      time.Duration
	P95Latency      time.Duration
	P99Latency      time.Duration
	SuccessRate     float64
	AvgProcessingMs float64
}

// ProcessFrameRequest запрос
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
}

func benchmark(name, url string, token string, requests int, concurrency int, reqBody []byte) BenchmarkResult {
	var wg sync.WaitGroup
	var mu sync.Mutex

	latencies := make([]time.Duration, 0, requests)
	processingTimes := make([]float64, 0, requests)
	successCount := 0

	jobs := make(chan int, requests)
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)

	start := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			client := &http.Client{
				Timeout: 30 * time.Second,
			}

			for range jobs {
				reqStart := time.Now()
				req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
				req.Header.Set("Content-Type", "application/json")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				resp, err := client.Do(req)
				latency := time.Since(reqStart)

				if err == nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()

					var result ProcessFrameResponse
					if json.Unmarshal(body, &result) == nil && result.Success {
						mu.Lock()
						successCount++
						latencies = append(latencies, latency)
						processingTimes = append(processingTimes, result.ProcessingTimeMs)
						mu.Unlock()
					}
				}
			}
		}()
	}

	wg.Wait()
	duration := time.Since(start)

	// Вычисляем статистику
	result := BenchmarkResult{
		Name:          name,
		TotalRequests: requests,
		Duration:      duration,
		RPS:           float64(requests) / duration.Seconds(),
		SuccessRate:   float64(successCount) / float64(requests) * 100,
	}

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool {
			return latencies[i] < latencies[j]
		})

		var total time.Duration
		for _, l := range latencies {
			total += l
		}
		result.AvgLatency = total / time.Duration(len(latencies))
		result.MinLatency = latencies[0]
		result.MaxLatency = latencies[len(latencies)-1]
		result.P50Latency = latencies[int(float64(len(latencies))*0.50)]
		result.P95Latency = latencies[int(float64(len(latencies))*0.95)]
		result.P99Latency = latencies[int(math.Min(float64(len(latencies))*0.99, float64(len(latencies)-1)))]
	}

	if len(processingTimes) > 0 {
		var total float64
		for _, t := range processingTimes {
			total += t
		}
		result.AvgProcessingMs = total / float64(len(processingTimes))
	}

	return result
}

func printResults(results []BenchmarkResult) {
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                         BENCHMARK RESULTS COMPARISON                           ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════════════════════╣")

	for _, r := range results {
		fmt.Printf("║ %-78s ║\n", r.Name)
		fmt.Println("╠────────────────────────────────────────────────────────────────────────────────╣")
		fmt.Printf("║  Total Requests:     %-57d ║\n", r.TotalRequests)
		fmt.Printf("║  Duration:           %-57s ║\n", r.Duration.Round(time.Millisecond))
		fmt.Printf("║  RPS:                %-57.1f ║\n", r.RPS)
		fmt.Printf("║  Success Rate:       %-57.1f%% ║\n", r.SuccessRate)
		fmt.Printf("║  Avg Processing:     %-57.2f ms ║\n", r.AvgProcessingMs)
		fmt.Println("║  Latency:                                                                      ║")
		fmt.Printf("║    Min:              %-57s ║\n", r.MinLatency.Round(time.Microsecond))
		fmt.Printf("║    Avg:              %-57s ║\n", r.AvgLatency.Round(time.Microsecond))
		fmt.Printf("║    P50:              %-57s ║\n", r.P50Latency.Round(time.Microsecond))
		fmt.Printf("║    P95:              %-57s ║\n", r.P95Latency.Round(time.Microsecond))
		fmt.Printf("║    P99:              %-57s ║\n", r.P99Latency.Round(time.Microsecond))
		fmt.Printf("║    Max:              %-57s ║\n", r.MaxLatency.Round(time.Microsecond))
		fmt.Println("╠════════════════════════════════════════════════════════════════════════════════╣")
	}

	// Сравнение
	if len(results) >= 2 {
		arena := results[0]
		heap := results[1]

		fmt.Println("║                              COMPARISON                                        ║")
		fmt.Println("╠────────────────────────────────────────────────────────────────────────────────╣")

		rpsRatio := arena.RPS / heap.RPS
		if rpsRatio > 1 {
			fmt.Printf("║  Arena mode is %.1fx faster in throughput (RPS)                               ║\n", rpsRatio)
		} else {
			fmt.Printf("║  No Arena mode is %.1fx faster in throughput (RPS)                            ║\n", 1/rpsRatio)
		}

		latRatio := float64(heap.AvgLatency) / float64(arena.AvgLatency)
		if latRatio > 1 {
			fmt.Printf("║  Arena mode has %.1fx lower average latency                                   ║\n", latRatio)
		} else {
			fmt.Printf("║  No Arena mode has %.1fx lower average latency                                ║\n", 1/latRatio)
		}

		procRatio := heap.AvgProcessingMs / arena.AvgProcessingMs
		if procRatio > 1 {
			fmt.Printf("║  Arena processing is %.1fx faster                                             ║\n", procRatio)
		} else {
			fmt.Printf("║  No Arena processing is %.1fx faster                                          ║\n", 1/procRatio)
		}
	}

	fmt.Println("╚════════════════════════════════════════════════════════════════════════════════╝")
}

func main() {
	url := flag.String("url", "http://localhost:8080/api/process", "Gateway process URL")
	token := flag.String("token", "", "Bearer token for Gateway API")
	requests := flag.Int("requests", 1000, "Number of requests per benchmark")
	concurrency := flag.Int("concurrency", 10, "Number of concurrent workers")
	width := flag.Int("width", 640, "Frame width")
	height := flag.Int("height", 480, "Frame height")
	warmup := flag.Int("warmup", 100, "Warmup requests")
	flag.Parse()

	log.Println("=== Streaming Processor Benchmark ===")
	log.Printf("Requests per test: %d", *requests)
	log.Printf("Concurrency: %d", *concurrency)
	log.Printf("Frame size: %dx%d", *width, *height)
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
		Filter:     "gaussian_blur",
		BlurRadius: 5.0,
		MemoryMode: "arena",
	}
	reqBody, _ := json.Marshal(req)

	var results []BenchmarkResult

	log.Printf("Warming up Arena mode (%d requests)...", *warmup)
	benchmark("Warmup Arena", *url, *token, *warmup, *concurrency, reqBody)

	log.Printf("Benchmarking Arena mode (%d requests)...", *requests)
	arenaResult := benchmark("C++ Processor (Memory Arena)", *url, *token, *requests, *concurrency, reqBody)
	results = append(results, arenaResult)
	log.Printf("Arena: %.1f RPS, %.2f ms avg latency", arenaResult.RPS, float64(arenaResult.AvgLatency.Microseconds())/1000)

	// Небольшая пауза
	time.Sleep(2 * time.Second)

	req.MemoryMode = "no_arena"
	reqBody, _ = json.Marshal(req)

	log.Printf("Warming up No Arena mode (%d requests)...", *warmup)
	benchmark("Warmup No Arena", *url, *token, *warmup, *concurrency, reqBody)

	log.Printf("Benchmarking No Arena mode (%d requests)...", *requests)
	heapResult := benchmark("C++ Processor (No Arena)", *url, *token, *requests, *concurrency, reqBody)
	results = append(results, heapResult)
	log.Printf("No Arena: %.1f RPS, %.2f ms avg latency", heapResult.RPS, float64(heapResult.AvgLatency.Microseconds())/1000)

	// Выводим результаты
	printResults(results)
}
