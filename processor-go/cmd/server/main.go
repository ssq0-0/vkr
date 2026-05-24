package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	pb "github.com/streaming-system/processor-go/proto"
	"google.golang.org/grpc"
)

// Глобальная статистика
var globalStats = &struct {
	totalFrames   int64
	totalTimeNs   int64
	totalAllocs   int64
	totalPoolGets int64
	totalPoolPuts int64
	startTime     time.Time
}{}

// Пулы буферов для переиспользования памяти (замена Arena)
var (
	// Пул для буферов кадров (640x480x3 = 921600 байт)
	frameBufferPool = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, 640*480*3)
			return &buf
		},
	}

	// Пул для временных буферов (float)
	tempBufferPool = sync.Pool{
		New: func() interface{} {
			buf := make([]float32, 640*480*3)
			return &buf
		},
	}

	// Пул для ядер свертки
	kernelPool = sync.Pool{
		New: func() interface{} {
			buf := make([]float32, 31)
			return &buf
		},
	}
)

// ImageProcessor обработчик изображений на Go
type ImageProcessor struct{}

// NewImageProcessor создает новый обработчик
func NewImageProcessor() *ImageProcessor {
	return &ImageProcessor{}
}

// GaussianBlur применяет размытие по Гауссу
func (p *ImageProcessor) GaussianBlur(input []byte, width, height int, radius float32) []byte {
	size := width * height * 3
	
	// Создаём буферы нужного размера
	output := make([]byte, size)
	temp := make([]float32, size)
	
	// Получаем ядро из пула
	kernelPtr := kernelPool.Get().(*[]float32)
	kernel := *kernelPtr
	atomic.AddInt64(&globalStats.totalPoolGets, 1)

	// Вычисляем размер ядра
	kernelSize := int(math.Ceil(float64(radius)*6)) | 1
	if kernelSize < 3 {
		kernelSize = 3
	}
	if kernelSize > 31 {
		kernelSize = 31
	}

	// Генерируем ядро Гаусса
	generateGaussianKernel(kernel[:kernelSize], radius)

	// Применяем сепарабельную свертку
	channels := 3
	half := kernelSize / 2

	// Горизонтальный проход
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var sumR, sumG, sumB float32
			for k := 0; k < kernelSize; k++ {
				kx := x - half + k
				if kx < 0 {
					kx = 0
				}
				if kx >= width {
					kx = width - 1
				}
				idx := (y*width + kx) * channels
				if idx+2 < len(input) {
					sumR += float32(input[idx]) * kernel[k]
					sumG += float32(input[idx+1]) * kernel[k]
					sumB += float32(input[idx+2]) * kernel[k]
				}
			}
			outIdx := (y*width + x) * channels
			if outIdx+2 < len(temp) {
				temp[outIdx] = sumR
				temp[outIdx+1] = sumG
				temp[outIdx+2] = sumB
			}
		}
	}

	// Вертикальный проход
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var sumR, sumG, sumB float32
			for k := 0; k < kernelSize; k++ {
				ky := y - half + k
				if ky < 0 {
					ky = 0
				}
				if ky >= height {
					ky = height - 1
				}
				idx := (ky*width + x) * channels
				if idx+2 < len(temp) {
					sumR += temp[idx] * kernel[k]
					sumG += temp[idx+1] * kernel[k]
					sumB += temp[idx+2] * kernel[k]
				}
			}
			outIdx := (y*width + x) * channels
			if outIdx+2 < len(output) {
				output[outIdx] = clampToByte(sumR)
				output[outIdx+1] = clampToByte(sumG)
				output[outIdx+2] = clampToByte(sumB)
			}
		}
	}

	// Возвращаем ядро в пул
	kernelPool.Put(kernelPtr)
	atomic.AddInt64(&globalStats.totalPoolPuts, 1)

	return output
}

// Grayscale конвертирует в градации серого
func (p *ImageProcessor) Grayscale(input []byte, width, height int) []byte {
	output := make([]byte, len(input))
	size := width * height

	for i := 0; i < size; i++ {
		idx := i * 3
		gray := byte(0.299*float32(input[idx]) +
			0.587*float32(input[idx+1]) +
			0.114*float32(input[idx+2]))
		output[idx] = gray
		output[idx+1] = gray
		output[idx+2] = gray
	}

	return output
}

// Sharpen применяет повышение резкости
func (p *ImageProcessor) Sharpen(input []byte, width, height int, intensity float32) []byte {
	output := make([]byte, len(input))
	channels := 3

	// Ядро повышения резкости
	kernel := []float32{
		0, -intensity, 0,
		-intensity, 1 + 4*intensity, -intensity,
		0, -intensity, 0,
	}

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			for c := 0; c < channels; c++ {
				var sum float32
				for ky := -1; ky <= 1; ky++ {
					for kx := -1; kx <= 1; kx++ {
						idx := ((y+ky)*width + (x + kx)) * channels
						sum += float32(input[idx+c]) * kernel[(ky+1)*3+(kx+1)]
					}
				}
				outIdx := (y*width + x) * channels
				output[outIdx+c] = clampToByte(sum)
			}
		}
	}

	return output
}

// EdgeDetect применяет детекцию границ (Sobel)
func (p *ImageProcessor) EdgeDetect(input []byte, width, height int) []byte {
	output := make([]byte, len(input))
	channels := 3

	// Сначала конвертируем в градации серого
	gray := make([]byte, width*height)
	for i := 0; i < width*height; i++ {
		idx := i * 3
		gray[i] = byte(0.299*float32(input[idx]) +
			0.587*float32(input[idx+1]) +
			0.114*float32(input[idx+2]))
	}

	// Sobel операторы
	sobelX := []int{-1, 0, 1, -2, 0, 2, -1, 0, 1}
	sobelY := []int{-1, -2, -1, 0, 0, 0, 1, 2, 1}

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			gx, gy := 0, 0
			for ky := -1; ky <= 1; ky++ {
				for kx := -1; kx <= 1; kx++ {
					pixel := int(gray[(y+ky)*width+(x+kx)])
					gx += pixel * sobelX[(ky+1)*3+(kx+1)]
					gy += pixel * sobelY[(ky+1)*3+(kx+1)]
				}
			}
			magnitude := int(math.Sqrt(float64(gx*gx + gy*gy)))
			if magnitude > 255 {
				magnitude = 255
			}

			outIdx := (y*width + x) * channels
			output[outIdx] = byte(magnitude)
			output[outIdx+1] = byte(magnitude)
			output[outIdx+2] = byte(magnitude)
		}
	}

	return output
}

func generateGaussianKernel(kernel []float32, sigma float32) {
	size := len(kernel)
	half := size / 2
	sigma2 := 2 * sigma * sigma
	var sum float32

	for i := 0; i < size; i++ {
		x := float32(i - half)
		kernel[i] = float32(math.Exp(float64(-x * x / sigma2)))
		sum += kernel[i]
	}

	// Нормализация
	for i := 0; i < size; i++ {
		kernel[i] /= sum
	}
}

func clampToByte(v float32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// StreamingProcessorServer gRPC сервер
type StreamingProcessorServer struct {
	pb.UnimplementedStreamingProcessorServer
	processor *ImageProcessor
}

// NewStreamingProcessorServer создает новый сервер
func NewStreamingProcessorServer() *StreamingProcessorServer {
	return &StreamingProcessorServer{
		processor: NewImageProcessor(),
	}
}

// ProcessMediaStream обрабатывает поток медиа-данных (bidirectional streaming)
func (s *StreamingProcessorServer) ProcessMediaStream(stream pb.StreamingProcessor_ProcessMediaStreamServer) error {
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// Обрабатываем
		result := s.processChunk(chunk)

		// Отправляем результат
		if err := stream.Send(result); err != nil {
			return err
		}
	}
}

func (s *StreamingProcessorServer) processChunk(chunk *pb.MediaChunk) *pb.ProcessedChunk {
	start := time.Now()

	result := &pb.ProcessedChunk{
		SequenceId: chunk.SequenceId,
	}

	// Проверяем тип
	if chunk.Type != pb.MediaType_MEDIA_TYPE_VIDEO && chunk.Type != pb.MediaType_MEDIA_TYPE_IMAGE {
		result.Success = false
		result.ErrorMessage = "Unsupported media type"
		return result
	}

	width := int(chunk.Width)
	height := int(chunk.Height)
	expectedSize := width * height * 3

	if len(chunk.Data) != expectedSize {
		result.Success = false
		result.ErrorMessage = fmt.Sprintf("Invalid data size: expected %d, got %d", expectedSize, len(chunk.Data))
		return result
	}

	// Определяем фильтр
	filter := pb.FilterType_FILTER_TYPE_GAUSSIAN_BLUR
	blurRadius := float32(5.0)
	intensity := float32(1.0)

	if chunk.Params != nil {
		filter = chunk.Params.Filter
		if chunk.Params.BlurRadius > 0 {
			blurRadius = chunk.Params.BlurRadius
		}
		if chunk.Params.Intensity > 0 {
			intensity = chunk.Params.Intensity
		}
	}

	// Обрабатываем
	var output []byte
	switch filter {
	case pb.FilterType_FILTER_TYPE_GAUSSIAN_BLUR:
		output = s.processor.GaussianBlur(chunk.Data, width, height, blurRadius)
	case pb.FilterType_FILTER_TYPE_GRAYSCALE:
		output = s.processor.Grayscale(chunk.Data, width, height)
	case pb.FilterType_FILTER_TYPE_SHARPEN:
		output = s.processor.Sharpen(chunk.Data, width, height, intensity)
	case pb.FilterType_FILTER_TYPE_EDGE_DETECT:
		output = s.processor.EdgeDetect(chunk.Data, width, height)
	default:
		output = s.processor.GaussianBlur(chunk.Data, width, height, blurRadius)
	}

	result.Data = output
	result.Success = true
	result.ProcessingTimeNs = time.Since(start).Nanoseconds()

	// Обновляем статистику
	atomic.AddInt64(&globalStats.totalFrames, 1)
	atomic.AddInt64(&globalStats.totalTimeNs, result.ProcessingTimeNs)
	atomic.AddInt64(&globalStats.totalAllocs, 1)

	// Добавляем метрики памяти
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	result.MemoryMetrics = &pb.MemoryMetrics{
		HeapUsedBytes:    int64(m.HeapAlloc),
		TotalAllocations: int64(m.Mallocs),
	}

	framesProcessed.Inc()
	processingTime.Observe(float64(result.ProcessingTimeNs) / 1e9)

	return result
}

// GetStats возвращает статистику
func (s *StreamingProcessorServer) GetStats(ctx context.Context, req *pb.StatsRequest) (*pb.StatsResponse, error) {
	totalFrames := atomic.LoadInt64(&globalStats.totalFrames)
	totalTimeNs := atomic.LoadInt64(&globalStats.totalTimeNs)

	var avgTimeMs float64
	if totalFrames > 0 {
		avgTimeMs = float64(totalTimeNs) / float64(totalFrames) / 1e6
	}

	elapsed := time.Since(globalStats.startTime).Seconds()
	var fps float64
	if elapsed > 0 {
		fps = float64(totalFrames) / elapsed
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Единый StatsResponse: поля *arena* в protobuf заполняем из Go heap (не Memory Arena).
	return &pb.StatsResponse{
		TotalFramesProcessed:  totalFrames,
		TotalProcessingTimeNs: totalTimeNs,
		AvgProcessingTimeMs:   avgTimeMs,
		CurrentArenaSizeBytes: int64(m.HeapAlloc),
		PeakArenaSizeBytes:    int64(m.HeapSys),
		TotalArenaAllocations: atomic.LoadInt64(&globalStats.totalAllocs),
		TotalArenaResets:      atomic.LoadInt64(&globalStats.totalPoolPuts),
		FramesPerSecond:       fps,
	}, nil
}

// Prometheus метрики
var (
	framesProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "processor_go_frames_processed_total",
		Help: "Total number of frames processed",
	})
	processingTime = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "processor_go_processing_time_seconds",
		Help:    "Frame processing time",
		Buckets: prometheus.ExponentialBuckets(0.0001, 2, 15),
	})
	heapAlloc = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "processor_go_heap_alloc_bytes",
		Help: "Current heap allocation",
	})
	poolGets = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "processor_go_pool_gets_total",
		Help: "Total sync.Pool Get operations",
	})
	poolPuts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "processor_go_pool_puts_total",
		Help: "Total sync.Pool Put operations",
	})
)

func init() {
	prometheus.MustRegister(framesProcessed, processingTime, heapAlloc, poolGets, poolPuts)
}

func main() {
	port := flag.Int("port", 9090, "gRPC server port")
	metricsPort := flag.Int("metrics-port", 9091, "Prometheus metrics port")
	flag.Parse()

	// Переменные окружения
	if envPort := os.Getenv("PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", port)
	}

	globalStats.startTime = time.Now()

	log.Println("=== Streaming Processor Service (Go with sync.Pool) ===")
	log.Printf("gRPC Port: %d", *port)
	log.Printf("Metrics Port: %d", *metricsPort)
	log.Printf("GOMAXPROCS: %d", runtime.GOMAXPROCS(0))

	// Запускаем сервер метрик
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		
		// Детальный runtime endpoint
		mux.HandleFunc("/runtime", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Content-Type", "application/json")
			
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			
			totalFrames := atomic.LoadInt64(&globalStats.totalFrames)
			totalTimeNs := atomic.LoadInt64(&globalStats.totalTimeNs)
			poolGets := atomic.LoadInt64(&globalStats.totalPoolGets)
			poolPuts := atomic.LoadInt64(&globalStats.totalPoolPuts)
			
			var avgTimeMs float64
			if totalFrames > 0 {
				avgTimeMs = float64(totalTimeNs) / float64(totalFrames) / 1e6
			}
			
			elapsed := time.Since(globalStats.startTime).Seconds()
			var fps float64
			if elapsed > 0 {
				fps = float64(totalFrames) / elapsed
			}
			
			// Детальная JSON статистика
			fmt.Fprintf(w, `{
				"process": {
					"uptime_seconds": %.1f,
					"goroutines": %d,
					"cgo_calls": %d
				},
				"memory": {
					"heap_alloc_bytes": %d,
					"heap_sys_bytes": %d,
					"heap_idle_bytes": %d,
					"heap_inuse_bytes": %d,
					"heap_objects": %d,
					"stack_inuse_bytes": %d,
					"total_alloc_bytes": %d
				},
				"gc": {
					"num_gc": %d,
					"pause_total_ns": %d,
					"last_pause_ns": %d,
					"next_gc_bytes": %d,
					"gc_cpu_percent": %.4f
				},
				"pool": {
					"gets": %d,
					"puts": %d,
					"reuse_ratio": %.2f
				},
				"processing": {
					"total_frames": %d,
					"fps": %.1f,
					"avg_latency_ms": %.2f,
					"total_time_ns": %d
				}
			}`,
				elapsed,
				runtime.NumGoroutine(),
				runtime.NumCgoCall(),
				m.HeapAlloc,
				m.HeapSys,
				m.HeapIdle,
				m.HeapInuse,
				m.HeapObjects,
				m.StackInuse,
				m.TotalAlloc,
				m.NumGC,
				m.PauseTotalNs,
				m.PauseNs[(m.NumGC+255)%256],
				m.NextGC,
				m.GCCPUFraction,
				poolGets,
				poolPuts,
				func() float64 { if poolGets > 0 { return float64(poolPuts) / float64(poolGets) }; return 0 }(),
				totalFrames,
				fps,
				avgTimeMs,
				totalTimeNs)
		})
		
		mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Content-Type", "application/json")
			server := NewStreamingProcessorServer()
			stats, _ := server.GetStats(context.Background(), &pb.StatsRequest{})
			fmt.Fprintf(w, `{"totalFramesProcessed":%d,"avgProcessingTimeMs":%.2f,"framesPerSecond":%.1f,"heapBytes":%d}`,
				stats.TotalFramesProcessed,
				stats.AvgProcessingTimeMs,
				stats.FramesPerSecond,
				stats.CurrentArenaSizeBytes)
		})
		
		log.Printf("Metrics server listening on :%d", *metricsPort)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", *metricsPort), mux); err != nil {
			log.Printf("Metrics server error: %v", err)
		}
	}()

	// Обновляем метрики периодически
	go func() {
		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			heapAlloc.Set(float64(m.HeapAlloc))
		}
	}()

	// Создаем gRPC сервер
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(100*1024*1024),
		grpc.MaxSendMsgSize(100*1024*1024),
	)

	// Регистрируем сервис
	server := NewStreamingProcessorServer()
	pb.RegisterStreamingProcessorServer(grpcServer, server)

	log.Printf("gRPC server listening on :%d", *port)

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down...")
	grpcServer.GracefulStop()
	log.Println("Server stopped")
}
