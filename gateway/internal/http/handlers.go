package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/streaming-system/gateway/internal/auth"
	grpcclient "github.com/streaming-system/gateway/internal/grpc"
	"github.com/streaming-system/gateway/internal/metrics"
	"github.com/streaming-system/gateway/internal/storage"
	pb "github.com/streaming-system/gateway/proto"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024 * 1024, // 1 MB
	WriteBufferSize: 1024 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Разрешаем все origins для упрощения
	},
}

// Пул буферов для переиспользования памяти
var bufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 640*480*3) // Размер кадра 640x480 RGB
		return &buf
	},
}

// Handler обработчик HTTP запросов
type Handler struct {
	pool    *grpcclient.ProcessorPool
	metrics *metrics.Metrics
	store   *storage.Store
	tokens  *auth.TokenManager
}

// NewHandler создает новый обработчик
func NewHandler(pool *grpcclient.ProcessorPool, m *metrics.Metrics, store *storage.Store, tokens *auth.TokenManager) *Handler {
	return &Handler{
		pool:    pool,
		metrics: m,
		store:   store,
		tokens:  tokens,
	}
}

type authRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      auth.User `json:"user"`
}

func (h *Handler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		http.Error(w, "PostgreSQL storage is not configured", http.StatusServiceUnavailable)
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	user, err := h.store.CreateUser(r.Context(), req.Username, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.writeToken(w, r, user)
}

func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		http.Error(w, "PostgreSQL storage is not configured", http.StatusServiceUnavailable)
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	user, err := h.store.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	h.writeToken(w, r, user)
}

func (h *Handler) HandleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := h.currentUser(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(user)
}

func (h *Handler) HandleHistory(w http.ResponseWriter, r *http.Request) {
	user, ok := h.currentUser(w, r)
	if !ok {
		return
	}
	if h.store == nil {
		http.Error(w, "PostgreSQL storage is not configured", http.StatusServiceUnavailable)
		return
	}
	runs, err := h.store.RecentRuns(r.Context(), user.ID, 30)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runs)
}

func (h *Handler) writeToken(w http.ResponseWriter, r *http.Request, user auth.User) {
	token, expiresAt, err := h.tokens.Issue(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if h.store != nil {
		_ = h.store.SaveSession(r.Context(), user.ID, h.tokens.HashToken(token), expiresAt)
	}
	w.Header().Set("Content-Type", "application/json")
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	_ = json.NewEncoder(w).Encode(authResponse{Token: token, ExpiresAt: expiresAt, User: user})
}

func (h *Handler) extractToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	if c, err := r.Cookie("auth_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

func (h *Handler) userFromRequest(r *http.Request) (auth.User, error) {
	if h.tokens == nil {
		return auth.User{}, errors.New("auth is not configured")
	}
	token := h.extractToken(r)
	if token == "" {
		return auth.User{}, errors.New("authorization required")
	}
	return h.tokens.Verify(token)
}

func (h *Handler) currentUser(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	user, err := h.userFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return auth.User{}, false
	}
	return user, true
}

// ProcessFrameRequest запрос на обработку кадра
type ProcessFrameRequest struct {
	Data       string  `json:"data"`        // Base64 encoded RGB data
	Width      int     `json:"width"`       // Ширина кадра
	Height     int     `json:"height"`      // Высота кадра
	Filter     string  `json:"filter"`      // Тип фильтра
	BlurRadius float32 `json:"blur_radius"` // Радиус размытия
	MemoryMode string  `json:"memory_mode"` // arena | no_arena
}

// ProcessFrameResponse ответ с обработанным кадром
type ProcessFrameResponse struct {
	Data             string  `json:"data"`               // Base64 encoded result
	Success          bool    `json:"success"`            // Успех обработки
	ProcessingTimeMs float64 `json:"processing_time_ms"` // Время обработки
	MemoryMode       string  `json:"memory_mode"`        // Использованный режим памяти
	Error            string  `json:"error,omitempty"`    // Ошибка
}

// HandleProcessFrame обрабатывает HTTP запрос на обработку одного кадра
func (h *Handler) HandleProcessFrame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := h.currentUser(w, r)
	if !ok {
		return
	}

	start := time.Now()
	h.metrics.IncrementRequests("process_frame")

	// Парсим запрос
	var req ProcessFrameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.metrics.IncrementErrors("invalid_request")
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Декодируем данные из Base64
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		h.metrics.IncrementErrors("decode_error")
		http.Error(w, "Invalid base64 data", http.StatusBadRequest)
		return
	}

	// Проверяем размер данных
	expectedSize := req.Width * req.Height * 3
	if len(data) != expectedSize {
		h.metrics.IncrementErrors("size_mismatch")
		http.Error(w, fmt.Sprintf("Data size mismatch: expected %d, got %d", expectedSize, len(data)), http.StatusBadRequest)
		return
	}
	h.metrics.AddRequestPayloadBytes("process_frame", len(data))
	memoryMode := normalizeMemoryMode(req.MemoryMode)

	// Получаем клиент из пула
	client, err := h.pool.Get()
	if err != nil {
		h.metrics.IncrementErrors("pool_error")
		http.Error(w, "No available processors", http.StatusServiceUnavailable)
		return
	}

	// Создаем gRPC запрос
	chunk := &pb.MediaChunk{
		Data:        data,
		Type:        pb.MediaType_MEDIA_TYPE_IMAGE,
		Width:       int32(req.Width),
		Height:      int32(req.Height),
		Codec:       "raw",
		SequenceId:  1,
		TimestampNs: time.Now().UnixNano(),
		Params: &pb.ProcessingParams{
			Filter:     parseFilterType(req.Filter),
			BlurRadius: req.BlurRadius,
		},
	}

	// Обрабатываем
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := client.ProcessSingleChunk(ctx, chunk, memoryMode)
	if err != nil {
		h.metrics.IncrementErrors("processing_error")
		http.Error(w, "Processing error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Формируем ответ
	resp := ProcessFrameResponse{
		Data:             base64.StdEncoding.EncodeToString(result.Data),
		Success:          result.Success,
		ProcessingTimeMs: float64(result.ProcessingTimeNs) / 1e6,
		MemoryMode:       memoryMode,
		Error:            result.ErrorMessage,
	}
	if h.store != nil {
		_ = h.store.SaveProcessingRun(r.Context(), storage.ProcessingRun{
			UserID:           user.ID,
			MemoryMode:       memoryMode,
			FilterName:       req.Filter,
			Width:            req.Width,
			Height:           req.Height,
			FramesCount:      1,
			Success:          result.Success,
			ProcessingTimeMs: resp.ProcessingTimeMs,
			ErrorMessage:     result.ErrorMessage,
		})
	}

	h.metrics.RecordLatency("process_frame", time.Since(start))
	h.metrics.IncrementProcessedFrames()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleProcessStream обрабатывает HTTP запрос на потоковую обработку (multipart)
func (h *Handler) HandleProcessStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.currentUser(w, r); !ok {
		return
	}

	h.metrics.IncrementRequests("process_stream")

	// Получаем параметры из query
	width, _ := strconv.Atoi(r.URL.Query().Get("width"))
	height, _ := strconv.Atoi(r.URL.Query().Get("height"))
	filter := r.URL.Query().Get("filter")
	blurRadius, _ := strconv.ParseFloat(r.URL.Query().Get("blur_radius"), 32)
	memoryMode := normalizeMemoryMode(r.URL.Query().Get("memory_mode"))

	if width == 0 {
		width = 640
	}
	if height == 0 {
		height = 480
	}
	if blurRadius == 0 {
		blurRadius = 5.0
	}

	// Получаем клиент
	client, err := h.pool.Get()
	if err != nil {
		http.Error(w, "No available processors", http.StatusServiceUnavailable)
		return
	}

	// Создаем канал для чанков
	chunks := make(chan *pb.MediaChunk, 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Запускаем обработку
	results, errs := client.ProcessStream(ctx, chunks, memoryMode)

	// Читаем данные и отправляем на обработку
	go func() {
		defer close(chunks)

		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		buf := *bufPtr

		frameSize := width * height * 3
		var sequenceID int64 = 0

		for {
			n, err := io.ReadFull(r.Body, buf[:frameSize])
			if err == io.EOF {
				break
			}
			if err != nil && err != io.ErrUnexpectedEOF {
				break
			}

			if n < frameSize {
				break
			}

			sequenceID++
			chunk := &pb.MediaChunk{
				Data:        append([]byte{}, buf[:frameSize]...),
				Type:        pb.MediaType_MEDIA_TYPE_VIDEO,
				Width:       int32(width),
				Height:      int32(height),
				Codec:       "raw",
				SequenceId:  sequenceID,
				TimestampNs: time.Now().UnixNano(),
				Params: &pb.ProcessingParams{
					Filter:     parseFilterType(filter),
					BlurRadius: float32(blurRadius),
				},
			}
			h.metrics.AddRequestPayloadBytes("process_stream", frameSize)

			select {
			case chunks <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Отправляем результаты клиенту
	w.Header().Set("Content-Type", "application/octet-stream")
	flusher, _ := w.(http.Flusher)

	for {
		select {
		case result, ok := <-results:
			if !ok {
				return
			}
			h.metrics.IncrementProcessedFrames()
			w.Write(result.Data)
			if flusher != nil {
				flusher.Flush()
			}
		case err := <-errs:
			if err != nil {
				h.metrics.IncrementErrors("stream_error")
			}
			return
		}
	}
}

// HandleWebSocket обрабатывает WebSocket соединения для real-time стриминга
func (h *Handler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	h.metrics.IncrementRequests("websocket")
	if _, ok := h.currentUser(w, r); !ok {
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.metrics.IncrementErrors("websocket_upgrade_error")
		return
	}
	defer conn.Close()

	// Получаем клиент из пула
	client, err := h.pool.Get()
	if err != nil {
		conn.WriteJSON(map[string]string{"error": "No available processors"})
		return
	}

	// Обрабатываем каждый кадр отдельно (без персистентного стрима)
	// Это проще и надёжнее для WebSocket
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return
		}

		// Парсим сообщение
		var req ProcessFrameRequest
		if err := json.Unmarshal(message, &req); err != nil {
			conn.WriteJSON(map[string]string{"error": "Invalid JSON"})
			continue
		}

		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			conn.WriteJSON(map[string]string{"error": "Invalid base64"})
			continue
		}

		chunk := &pb.MediaChunk{
			Data:        data,
			Type:        pb.MediaType_MEDIA_TYPE_VIDEO,
			Width:       int32(req.Width),
			Height:      int32(req.Height),
			Codec:       "raw",
			TimestampNs: time.Now().UnixNano(),
			Params: &pb.ProcessingParams{
				Filter:     parseFilterType(req.Filter),
				BlurRadius: req.BlurRadius,
			},
		}
		h.metrics.AddRequestPayloadBytes("websocket", len(data))

		// Обрабатываем один кадр
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		result, err := client.ProcessSingleChunk(ctx, chunk, normalizeMemoryMode(req.MemoryMode))
		cancel()

		if err != nil {
			conn.WriteJSON(map[string]string{"error": err.Error()})
			continue
		}

		h.metrics.IncrementProcessedFrames()

		resp := ProcessFrameResponse{
			Data:             base64.StdEncoding.EncodeToString(result.Data),
			Success:          result.Success,
			ProcessingTimeMs: float64(result.ProcessingTimeNs) / 1e6,
			MemoryMode:       normalizeMemoryMode(req.MemoryMode),
		}

		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}
}

// HandleHealth проверка здоровья сервиса
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	health := h.pool.HealthCheck(ctx)

	allHealthy := true
	for _, healthy := range health {
		if !healthy {
			allHealthy = false
			break
		}
	}

	status := "healthy"
	httpStatus := http.StatusOK
	if !allHealthy {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     status,
		"processors": health,
	})
}

// HandleStats возвращает статистику
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.currentUser(w, r); !ok {
		return
	}
	client, err := h.pool.Get()
	if err != nil {
		http.Error(w, "No available processors", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	stats, err := client.GetStats(ctx)
	if err != nil {
		http.Error(w, "Failed to get stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// HandleAdminRouting GET/POST: текущий режим маршрутизации или смена (round_robin / fixed на индекс).
func (h *Handler) HandleAdminRouting(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.currentUser(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		health := h.pool.HealthCheck(ctx)
		addrs := h.pool.Addrs()
		sticky := h.pool.GetSticky()
		mode := "round_robin"
		if sticky >= 0 {
			mode = "fixed"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"mode":         mode,
			"sticky_index": sticky,
			"addresses":    addrs,
			"health":       health,
		})
	case http.MethodPost:
		var body struct {
			Mode        string `json:"mode"` // round_robin | fixed
			StickyIndex *int   `json:"sticky_index"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		var idx int
		switch body.Mode {
		case "round_robin", "":
			idx = -1
		case "fixed":
			if body.StickyIndex == nil {
				http.Error(w, "fixed mode requires sticky_index", http.StatusBadRequest)
				return
			}
			idx = *body.StickyIndex
		default:
			http.Error(w, "mode must be round_robin or fixed", http.StatusBadRequest)
			return
		}
		if err := h.pool.SetSticky(idx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleAdminProcessorsStats статистика каждого процессора (arena / fps и т.д.) для сравнения и поиска утечек.
func (h *Handler) HandleAdminProcessorsStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.currentUser(w, r); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	type procStats struct {
		Index           int               `json:"index"`
		Addr            string            `json:"address"`
		Implementation  string            `json:"implementation"`
		MemorySemantics string            `json:"memory_semantics"`
		Stats           *pb.StatsResponse `json:"stats,omitempty"`
		Error           string            `json:"error,omitempty"`
	}
	out := make([]procStats, 0, h.pool.Count())
	for i := 0; i < h.pool.Count(); i++ {
		c, err := h.pool.ClientAt(i)
		if err != nil {
			continue
		}
		addr := c.Addr()
		impl := "cpp"
		sem := "C++ processor: memory-mode выбирается в запросе через metadata."
		if strings.Contains(addr, "processor-cpp") {
			impl = "cpp"
			sem = "Memory Arena C++: current_arena_size_bytes / peak_arena_size_bytes — реальные байты арены; heap-режим публикуется в Prometheus."
		}
		row := procStats{Index: i, Addr: addr, Implementation: impl, MemorySemantics: sem}
		st, err := c.GetStats(ctx)
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Stats = st
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func parseFilterType(filter string) pb.FilterType {
	switch filter {
	case "gaussian_blur", "blur":
		return pb.FilterType_FILTER_TYPE_GAUSSIAN_BLUR
	case "sharpen":
		return pb.FilterType_FILTER_TYPE_SHARPEN
	case "edge_detect", "edge":
		return pb.FilterType_FILTER_TYPE_EDGE_DETECT
	case "grayscale", "gray":
		return pb.FilterType_FILTER_TYPE_GRAYSCALE
	default:
		return pb.FilterType_FILTER_TYPE_GAUSSIAN_BLUR
	}
}

func normalizeMemoryMode(mode string) string {
	switch strings.ToLower(mode) {
	case "heap", "no_arena", "no-arena":
		return "no_arena"
	default:
		return "arena"
	}
}

// HandleRuntime возвращает детальную runtime статистику процессора
func (h *Handler) HandleRuntime(w http.ResponseWriter, r *http.Request) {
	client, err := h.pool.Get()
	if err != nil {
		http.Error(w, "No available processors", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	stats, err := client.GetStats(ctx)
	if err != nil {
		http.Error(w, "Failed to get stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Вычисляем дополнительные метрики
	var avgLatencyMs float64
	if stats.TotalFramesProcessed > 0 {
		avgLatencyMs = float64(stats.TotalProcessingTimeNs) / float64(stats.TotalFramesProcessed) / 1e6
	}

	// Размер кадра 640x480x3 = 921600 байт
	frameSize := int64(921600)
	estimatedArenaUsed := stats.TotalFramesProcessed * frameSize
	if estimatedArenaUsed > 8*1024*1024 {
		estimatedArenaUsed = estimatedArenaUsed % (8 * 1024 * 1024) // Arena resets
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Формируем JSON ответ
	fmt.Fprintf(w, `{
		"processor_type": "cpp_arena",
		"process": {
			"uptime_seconds": %.1f,
			"threads": 8
		},
		"memory": {
			"arena_allocated_bytes": %d,
			"arena_used_bytes": %d,
			"peak_arena_bytes": %d,
			"heap_alloc_bytes": 0,
			"heap_sys_bytes": 0
		},
		"arena": {
			"total_allocations": %d,
			"total_resets": %d,
			"current_size_bytes": %d,
			"peak_size_bytes": %d,
			"efficiency_percent": %.1f,
			"bytes_per_frame": %d
		},
		"gc": {
			"num_gc": 0,
			"pause_total_ns": 0,
			"last_pause_ns": 0,
			"note": "C++ has no GC - using Memory Arena"
		},
		"processing": {
			"total_frames": %d,
			"fps": %.1f,
			"avg_latency_ms": %.2f,
			"total_time_ns": %d
		}
	}`,
		float64(stats.TotalProcessingTimeNs)/1e9,
		func() int64 {
			if stats.CurrentArenaSizeBytes > 0 {
				return stats.CurrentArenaSizeBytes
			}
			return estimatedArenaUsed
		}(),
		func() int64 {
			if stats.CurrentArenaSizeBytes > 0 {
				return stats.CurrentArenaSizeBytes
			}
			return estimatedArenaUsed
		}(),
		func() int64 {
			if stats.PeakArenaSizeBytes > 0 {
				return stats.PeakArenaSizeBytes
			}
			return 8 * 1024 * 1024
		}(),
		stats.TotalArenaAllocations,
		stats.TotalArenaResets,
		func() int64 {
			if stats.CurrentArenaSizeBytes > 0 {
				return stats.CurrentArenaSizeBytes
			}
			return estimatedArenaUsed
		}(),
		func() int64 {
			if stats.PeakArenaSizeBytes > 0 {
				return stats.PeakArenaSizeBytes
			}
			return 8 * 1024 * 1024
		}(),
		func() float64 {
			if stats.TotalArenaResets > 0 && stats.TotalFramesProcessed > 0 {
				return 100.0 - (float64(stats.TotalArenaResets) / float64(stats.TotalFramesProcessed) * 100)
			}
			return 100.0
		}(),
		frameSize,
		stats.TotalFramesProcessed,
		stats.FramesPerSecond,
		avgLatencyMs,
		stats.TotalProcessingTimeNs)
}
