package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/streaming-system/gateway/internal/auth"
	grpcclient "github.com/streaming-system/gateway/internal/grpc"
	httphandler "github.com/streaming-system/gateway/internal/http"
	"github.com/streaming-system/gateway/internal/metrics"
	"github.com/streaming-system/gateway/internal/storage"
)

func main() {
	// Параметры командной строки
	httpPort := flag.Int("http-port", 8080, "HTTP server port")
	processorAddrs := flag.String("processors", "localhost:9090", "Comma-separated list of processor addresses")
	rateLimit := flag.Int("rate-limit", 1000, "Requests per second limit")
	flag.Parse()

	// Также поддерживаем переменные окружения
	if envPort := os.Getenv("HTTP_PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", httpPort)
	}
	if envProcessors := os.Getenv("PROCESSOR_ADDRS"); envProcessors != "" {
		*processorAddrs = envProcessors
	}

	log.Println("=== Gateway Service ===")
	log.Printf("HTTP Port: %d", *httpPort)
	log.Printf("Processor addresses: %s", *processorAddrs)
	log.Printf("Rate limit: %d rps", *rateLimit)

	// Создаем метрики
	m := metrics.NewMetrics()

	// Создаем пул соединений к процессорам
	addrs := strings.Split(*processorAddrs, ",")
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
	}

	pool, err := grpcclient.NewProcessorPool(addrs)
	if err != nil {
		log.Fatalf("Failed to create processor pool: %v", err)
	}
	defer pool.Close()

	m.SetProcessorPoolSize(len(addrs))
	log.Printf("Connected to %d processor(s)", len(addrs))

	var store *storage.Store
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		store, err = openStoreWithRetry(databaseURL, 12, 2*time.Second)
		if err != nil {
			log.Fatalf("Failed to connect PostgreSQL: %v", err)
		}
		defer store.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := store.Migrate(ctx); err != nil {
			cancel()
			log.Fatalf("Failed to migrate PostgreSQL schema: %v", err)
		}
		cancel()
		log.Println("PostgreSQL storage is connected")
	} else {
		log.Println("DATABASE_URL is empty: auth/history API will return service unavailable")
	}

	tokenManager := auth.NewTokenManager(os.Getenv("AUTH_SECRET"), 24*time.Hour)

	// Создаем обработчики
	handler := httphandler.NewHandler(pool, m, store, tokenManager)

	// Rate limiter
	rateLimiter := metrics.NewRateLimiter(*rateLimit)

	// Настраиваем маршруты
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/register", handler.HandleRegister)
	mux.HandleFunc("/api/login", handler.HandleLogin)
	mux.HandleFunc("/api/me", handler.HandleMe)
	mux.HandleFunc("/api/history", handler.HandleHistory)
	mux.HandleFunc("/api/process", handler.HandleProcessFrame)
	mux.HandleFunc("/process", handler.HandleProcessFrame) // Alias for demo
	mux.HandleFunc("/api/stream", handler.HandleProcessStream)
	mux.HandleFunc("/ws", handler.HandleWebSocket)

	// Health и метрики
	mux.HandleFunc("/health", handler.HandleHealth)
	mux.HandleFunc("/stats", handler.HandleStats)
	mux.HandleFunc("/runtime", handler.HandleRuntime) // Runtime stats for monitoring
	mux.HandleFunc("/api/admin/routing", handler.HandleAdminRouting)
	mux.HandleFunc("/api/admin/processors/stats", handler.HandleAdminProcessorsStats)
	mux.Handle("/metrics", m.Handler())

	mux.HandleFunc("/", handler.HandleRoot)
	mux.HandleFunc("/login", handler.HandleLoginPage)
	mux.HandleFunc("/logout", handler.HandleLogout)
	mux.HandleFunc("/panel", handler.HandlePanelPage([]byte(indexHTML)))
	mux.HandleFunc("/app", handler.HandlePanelPage([]byte(appHTML)))

	// Применяем middleware
	var finalHandler http.Handler = mux
	finalHandler = rateLimiter.Middleware(finalHandler)
	finalHandler = loggingMiddleware(finalHandler)
	finalHandler = corsMiddleware(finalHandler)

	// Создаем сервер
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", *httpPort),
		Handler:      finalHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Starting HTTP server on port %d", *httpPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	log.Println("Server stopped")
}

func openStoreWithRetry(databaseURL string, attempts int, delay time.Duration) (*storage.Store, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		store, err := storage.Open(ctx, databaseURL)
		cancel()
		if err == nil {
			return store, nil
		}
		lastErr = err
		log.Printf("PostgreSQL is not ready yet (%d/%d): %v", i+1, attempts, err)
		time.Sleep(delay)
	}
	return nil, lastErr
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s %v", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start))
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

const appHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Дипломная ИС обработки кадров</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 0; background: #101827; color: #e5e7eb; }
        main { max-width: 980px; margin: 0 auto; padding: 28px; }
        section { background: #172033; border: 1px solid #26344f; border-radius: 14px; padding: 18px; margin: 16px 0; }
        input, select, button { padding: 10px; border-radius: 8px; border: 1px solid #334155; margin: 4px; }
        input, select { background: #0f172a; color: #e5e7eb; }
        button { background: #2563eb; color: white; cursor: pointer; }
        pre { background: #0b1120; padding: 12px; border-radius: 10px; overflow: auto; }
        .row { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
        .muted { color: #94a3b8; }
    </style>
</head>
<body>
<main>
    <h1>Информационная система потоковой обработки кадров</h1>
    <p class="muted">Дипломный стенд: авторизация, PostgreSQL-журналирование, gRPC и C++ processor с режимами Memory Arena / No Arena.</p>

    <section>
        <h2>1. Авторизация</h2>
        <div class="row">
            <input id="username" placeholder="login" value="student">
            <input id="password" placeholder="password" type="password" value="student">
            <button onclick="register()">Регистрация</button>
            <button onclick="login()">Вход</button>
            <button onclick="me()">Проверить токен</button>
        </div>
        <p id="authStatus" class="muted">Токен отсутствует.</p>
    </section>

    <section>
        <h2>2. Обработка кадра</h2>
        <div class="row">
            <select id="filter">
                <option value="gaussian_blur">Gaussian blur</option>
                <option value="sharpen">Sharpen</option>
                <option value="edge_detect">Edge detect</option>
                <option value="grayscale">Grayscale</option>
            </select>
            <select id="memoryMode">
                <option value="arena">Memory Arena</option>
                <option value="no_arena">No Arena</option>
            </select>
            <input id="width" type="number" value="160" min="16">
            <input id="height" type="number" value="120" min="16">
            <button onclick="processFrame()">Обработать тестовый кадр</button>
        </div>
        <pre id="result">Результат появится здесь.</pre>
    </section>

    <section>
        <h2>3. История и статистика</h2>
        <button onclick="history()">История пользователя</button>
        <button onclick="stats()">Статистика процессора</button>
        <pre id="history">История появится здесь.</pre>
    </section>
</main>
<script>
let token = localStorage.getItem('token') || '';
updateAuthStatus();

function updateAuthStatus() {
    document.getElementById('authStatus').textContent = token ? 'Токен сохранён в браузере.' : 'Токен отсутствует.';
}

async function api(path, options = {}) {
    options.headers = options.headers || {};
    options.headers['Content-Type'] = 'application/json';
    if (token) options.headers['Authorization'] = 'Bearer ' + token;
    const resp = await fetch(path, options);
    const text = await resp.text();
    let data;
    try { data = JSON.parse(text); } catch { data = text; }
    if (!resp.ok) throw new Error(typeof data === 'string' ? data : JSON.stringify(data));
    return data;
}

async function register() {
    const body = credentials();
    const data = await api('/api/register', { method: 'POST', body: JSON.stringify(body) });
    token = data.token; localStorage.setItem('token', token); updateAuthStatus();
    document.getElementById('result').textContent = JSON.stringify(data, null, 2);
}

async function login() {
    const data = await api('/api/login', { method: 'POST', body: JSON.stringify(credentials()) });
    token = data.token; localStorage.setItem('token', token); updateAuthStatus();
    document.getElementById('result').textContent = JSON.stringify(data, null, 2);
}

async function me() {
    document.getElementById('result').textContent = JSON.stringify(await api('/api/me'), null, 2);
}

function credentials() {
    return { username: document.getElementById('username').value, password: document.getElementById('password').value };
}

function frameBase64(width, height) {
    const bytes = new Uint8Array(width * height * 3);
    for (let i = 0; i < bytes.length; i += 3) {
        bytes[i] = (i / 3) % 255;
        bytes[i + 1] = (i / 7) % 255;
        bytes[i + 2] = (i / 11) % 255;
    }
    let s = '';
    bytes.forEach(b => s += String.fromCharCode(b));
    return btoa(s);
}

async function processFrame() {
    const width = Number(document.getElementById('width').value);
    const height = Number(document.getElementById('height').value);
    const body = {
        data: frameBase64(width, height),
        width, height,
        filter: document.getElementById('filter').value,
        blur_radius: 3,
        memory_mode: document.getElementById('memoryMode').value
    };
    const data = await api('/api/process', { method: 'POST', body: JSON.stringify(body) });
    data.data = '<base64 image data omitted>';
    document.getElementById('result').textContent = JSON.stringify(data, null, 2);
}

async function history() {
    document.getElementById('history').textContent = JSON.stringify(await api('/api/history'), null, 2);
}

async function stats() {
    document.getElementById('history').textContent = JSON.stringify(await api('/stats'), null, 2);
}
</script>
</body>
</html>`

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Streaming Processor - Gateway</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        body {
            font-family: 'JetBrains Mono', 'Fira Code', monospace;
            background: linear-gradient(135deg, #1a1a2e 0%, #16213e 50%, #0f3460 100%);
            min-height: 100vh;
            color: #e0e0e0;
            padding: 2rem;
        }
        .container {
            max-width: 1200px;
            margin: 0 auto;
        }
        h1 {
            font-size: 2.5rem;
            margin-bottom: 1rem;
            background: linear-gradient(90deg, #00d4ff, #7b2cbf);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
            text-shadow: 0 0 30px rgba(0, 212, 255, 0.3);
        }
        .subtitle {
            color: #888;
            margin-bottom: 2rem;
            font-size: 0.9rem;
        }
        .card {
            background: rgba(255, 255, 255, 0.05);
            border: 1px solid rgba(255, 255, 255, 0.1);
            border-radius: 16px;
            padding: 1.5rem;
            margin-bottom: 1.5rem;
            backdrop-filter: blur(10px);
        }
        .card h2 {
            color: #00d4ff;
            font-size: 1.2rem;
            margin-bottom: 1rem;
            display: flex;
            align-items: center;
            gap: 0.5rem;
        }
        .endpoint {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 0.75rem 1rem;
            background: rgba(0, 0, 0, 0.3);
            border-radius: 8px;
            margin-bottom: 0.5rem;
            font-size: 0.9rem;
        }
        .method {
            padding: 0.25rem 0.5rem;
            border-radius: 4px;
            font-weight: bold;
            font-size: 0.75rem;
        }
        .method.post { background: #4CAF50; color: white; }
        .method.get { background: #2196F3; color: white; }
        .method.ws { background: #9C27B0; color: white; }
        code {
            color: #ff9800;
            font-family: inherit;
        }
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 1rem;
        }
        .stat-box {
            background: rgba(0, 212, 255, 0.1);
            border: 1px solid rgba(0, 212, 255, 0.3);
            border-radius: 12px;
            padding: 1rem;
            text-align: center;
        }
        .stat-value {
            font-size: 2rem;
            font-weight: bold;
            color: #00d4ff;
        }
        .stat-label {
            font-size: 0.8rem;
            color: #888;
            text-transform: uppercase;
        }
        #status {
            display: inline-block;
            width: 10px;
            height: 10px;
            border-radius: 50%;
            margin-right: 0.5rem;
        }
        #status.connected { background: #4CAF50; box-shadow: 0 0 10px #4CAF50; }
        #status.disconnected { background: #f44336; box-shadow: 0 0 10px #f44336; }
        .log-container {
            background: #000;
            border-radius: 8px;
            padding: 1rem;
            height: 200px;
            overflow-y: auto;
            font-size: 0.8rem;
        }
        .log-entry {
            margin: 0.25rem 0;
            color: #888;
        }
        .log-entry.success { color: #4CAF50; }
        .log-entry.error { color: #f44336; }
        .log-entry.info { color: #2196F3; }
        .control-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
            gap: 1rem;
            margin-bottom: 1rem;
        }
        label {
            display: block;
            color: #aaa;
            font-size: 0.8rem;
            margin-bottom: 0.35rem;
        }
        input, select {
            width: 100%;
            padding: 0.75rem;
            border-radius: 8px;
            border: 1px solid rgba(255, 255, 255, 0.18);
            background: rgba(0, 0, 0, 0.35);
            color: #e0e0e0;
            font-family: inherit;
        }
        .button-row {
            display: flex;
            flex-wrap: wrap;
            gap: 0.75rem;
            margin-bottom: 1rem;
        }
        button {
            border: none;
            border-radius: 8px;
            padding: 0.8rem 1rem;
            color: white;
            font-family: inherit;
            font-weight: bold;
            cursor: pointer;
            background: linear-gradient(90deg, #00a8ff, #7b2cbf);
        }
        button.secondary {
            background: rgba(255, 255, 255, 0.12);
            border: 1px solid rgba(255, 255, 255, 0.18);
        }
        button.danger {
            background: #d32f2f;
        }
        button:disabled {
            opacity: 0.45;
            cursor: not-allowed;
        }
        .hint {
            color: #888;
            font-size: 0.8rem;
            margin-bottom: 1rem;
            line-height: 1.45;
        }
        .preview-box {
            margin-top: 1rem;
        }
        .preview-box label {
            display: block;
            color: #00d4ff;
            font-size: 0.85rem;
            margin-bottom: 0.5rem;
        }
        #payloadPreview {
            background: #0a0a12;
            border: 1px solid rgba(0, 212, 255, 0.25);
            border-radius: 8px;
            padding: 0.75rem 1rem;
            font-size: 0.72rem;
            line-height: 1.45;
            white-space: pre-wrap;
            word-break: break-all;
            max-height: 220px;
            overflow-y: auto;
            color: #c8e6c9;
        }
        .mode-switch {
            display: flex;
            flex-wrap: wrap;
            gap: 0.5rem;
            margin-bottom: 1.25rem;
            padding: 0.35rem;
            background: rgba(0, 0, 0, 0.25);
            border-radius: 12px;
            border: 1px solid rgba(255, 255, 255, 0.12);
        }
        .mode-switch label {
            display: flex;
            align-items: center;
            gap: 0.5rem;
            cursor: pointer;
            padding: 0.55rem 1rem;
            border-radius: 8px;
            color: #ccc;
            font-size: 0.85rem;
            margin: 0;
        }
        .mode-switch label:hover {
            background: rgba(255, 255, 255, 0.06);
        }
        .mode-switch input {
            width: auto;
            accent-color: #00d4ff;
        }
        .mode-switch input:checked + span {
            color: #00d4ff;
            font-weight: bold;
        }
        .panel-section {
            display: none;
        }
        .panel-section.active {
            display: block;
        }
        .video-demo-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
            gap: 1rem;
            margin-top: 1rem;
        }
        .video-pane {
            text-align: center;
        }
        .video-pane h3 {
            font-size: 0.85rem;
            color: #888;
            margin-bottom: 0.5rem;
            font-weight: normal;
        }
        .video-pane video,
        .video-pane canvas {
            max-width: 100%;
            border-radius: 8px;
            border: 1px solid rgba(0, 212, 255, 0.35);
            background: #000;
        }
        .video-status {
            margin-top: 0.75rem;
            font-size: 0.8rem;
            color: #888;
        }
        .routing-pre {
            background: #0a0a12;
            border: 1px solid rgba(0, 212, 255, 0.2);
            border-radius: 8px;
            padding: 0.75rem 1rem;
            font-size: 0.75rem;
            color: #b0bec5;
            white-space: pre-wrap;
            margin-top: 0.75rem;
            max-height: 140px;
            overflow: auto;
        }
        .proc-admin-table {
            width: 100%;
            border-collapse: collapse;
            font-size: 0.78rem;
            margin-top: 0.5rem;
        }
        .proc-admin-table th,
        .proc-admin-table td {
            border: 1px solid rgba(255,255,255,0.12);
            padding: 0.45rem 0.6rem;
            text-align: left;
        }
        .proc-admin-table th {
            color: #00d4ff;
            font-weight: 600;
        }
        .proc-admin-table tr:nth-child(even) {
            background: rgba(0,0,0,0.2);
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Streaming Processor</h1>
        <p class="subtitle">Gateway for streaming large data chunks with gRPC bidirectional streaming · <a href="/logout" style="color:#00d4ff;">Выйти</a></p>
        
        <div class="card">
            <h2>📡 API Endpoints</h2>
            <div class="endpoint">
                <span><span class="method post">POST</span> <code>/api/process</code></span>
                <span>Process one generated message (JSON with Base64 data)</span>
            </div>
            <div class="endpoint">
                <span><span class="method post">POST</span> <code>/api/stream</code></span>
                <span>Stream processing (raw binary data)</span>
            </div>
            <div class="endpoint">
                <span><span class="method ws">WS</span> <code>/ws</code></span>
                <span>WebSocket real-time streaming</span>
            </div>
            <div class="endpoint">
                <span><span class="method get">GET</span> <code>/health</code></span>
                <span>Health check</span>
            </div>
            <div class="endpoint">
                <span><span class="method get">GET</span> <code>/stats</code></span>
                <span>Processor statistics</span>
            </div>
            <div class="endpoint">
                <span><span class="method get">GET</span> <code>/metrics</code></span>
                <span>Prometheus metrics</span>
            </div>
            <div class="endpoint">
                <span><span class="method get">GET</span> <code>/api/admin/routing</code></span>
                <span>Текущий режим маршрутизации и health по процессорам</span>
            </div>
            <div class="endpoint">
                <span><span class="method post">POST</span> <code>/api/admin/routing</code></span>
                <span><code>{"mode":"round_robin"}</code> или <code>{"mode":"fixed","sticky_index":0}</code></span>
            </div>
            <div class="endpoint">
                <span><span class="method get">GET</span> <code>/api/admin/processors/stats</code></span>
                <span>Сводная статистика по каждому процессору (arena, fps, …)</span>
            </div>
        </div>

        <div class="card">
            <h2><span id="status" class="disconnected"></span> System Status</h2>
            <div class="stats-grid">
                <div class="stat-box">
                    <div class="stat-value" id="fps">-</div>
                    <div class="stat-label">Messages/sec</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="latency">-</div>
                    <div class="stat-label">Avg Latency (ms)</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="processed">-</div>
                    <div class="stat-label">Messages Processed</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="arena">-</div>
                    <div class="stat-label">Память (MB) *</div>
                </div>
            </div>
        </div>

        <div class="card">
            <h2>Маршрутизация на процессор</h2>
            <p class="hint">
                Пул задаётся <code>PROCESSOR_ADDRS</code> (сейчас обычно два <strong>Go</strong> и один <strong>C++</strong>).
                По умолчанию — <strong>round-robin</strong> между всеми; режим «только выбранный» весь трафик ведёт на один gRPC-инстанс.
            </p>
            <div class="mode-switch" id="routingSwitch" role="radiogroup" aria-label="Процессор"></div>
            <div class="button-row">
                <button type="button" onclick="applyRouting()">Применить маршрут</button>
                <button type="button" class="secondary" onclick="refreshRoutingAndProcessors()">Обновить</button>
            </div>
            <pre id="routingStatus" class="routing-pre">Загрузка…</pre>
            <h3 style="margin:1rem 0 0.5rem;font-size:0.95rem;color:#888;">Статистика по каждому процессору (GetStats)</h3>
            <p class="hint" style="margin-bottom:0.75rem;">
                Колонки «текущая / peak память» читают поля protobuf <code>current_arena_size_bytes</code> и <code>peak_arena_size_bytes</code>.
                У <strong>C++</strong> это действительно arena; у <strong>Go</strong> туда подставлен <strong>heap</strong> (см. колонку «Бэкенд» и подсказку в таблице).
            </p>
            <div id="processorsTableWrap"></div>
        </div>

        <div class="card">
            <h2>Обработка данных</h2>
            <div class="mode-switch" role="radiogroup" aria-label="Режим демо">
                <label>
                    <input type="radio" name="mainMode" value="video" checked onchange="setMainMode('video')">
                    <span>Камера, кадры по WebSocket</span>
                </label>
                <label>
                    <input type="radio" name="mainMode" value="load" onchange="setMainMode('load')">
                    <span>Генератор нагрузки по HTTP</span>
                </label>
            </div>

            <div id="panelVideo" class="panel-section active">
                <p class="hint">
                    Классический режим: поток с веб-камеры, каждый кадр кодируется в RGB и отправляется на шлюз по WebSocket <code>/ws</code>;
                    ответ — обработанный кадр для предпросмотра.
                </p>
                <div class="control-grid">
                    <div>
                        <label for="filterVideo">Операция обработки</label>
                        <select id="filterVideo">
                            <option value="grayscale">grayscale</option>
                            <option value="gaussian_blur">gaussian_blur</option>
                            <option value="sharpen">sharpen</option>
                            <option value="edge_detect">edge_detect</option>
                        </select>
                    </div>
                    <div>
                        <label for="videoFps">Частота кадров (в сторону процессора)</label>
                        <input id="videoFps" type="number" min="1" max="30" value="8">
                    </div>
                </div>
                <div class="button-row">
                    <button id="videoStartBtn" type="button" onclick="startVideoDemo()">Включить камеру и стрим</button>
                    <button id="videoStopBtn" type="button" class="danger" onclick="stopVideoDemo()" disabled>Остановить</button>
                </div>
                <div class="video-demo-grid">
                    <div class="video-pane">
                        <h3>Источник (камера)</h3>
                        <video id="camVideo" playsinline muted autoplay width="320" height="240"></video>
                    </div>
                    <div class="video-pane">
                        <h3>После процессора</h3>
                        <canvas id="camOutCanvas" width="320" height="240"></canvas>
                    </div>
                </div>
                <canvas id="camCapCanvas" width="320" height="240" style="display:none"></canvas>
                <p class="video-status" id="videoStatus">Камера выключена.</p>
            </div>

            <div id="panelLoad" class="panel-section">
            <p class="hint">
                Generates a payload of the selected size and sends it to the processor.
                The current processor expects RGB-like chunks, so the sent payload is rounded up to a multiple of 3 bytes.
            </p>
            <div class="control-grid">
                <div>
                    <label for="messageSize">Message size, bytes</label>
                    <input id="messageSize" type="number" min="3" max="8388608" value="1048576">
                </div>
                <div>
                    <label for="rps">Target RPS for continuous mode</label>
                    <input id="rps" type="number" min="1" max="1000" value="20">
                </div>
                <div>
                    <label for="filter">Processing operation</label>
                    <select id="filter">
                        <option value="grayscale">grayscale</option>
                        <option value="gaussian_blur">gaussian_blur</option>
                        <option value="sharpen">sharpen</option>
                        <option value="edge_detect">edge_detect</option>
                    </select>
                </div>
            </div>
            <div class="button-row">
                <button id="sendOnceBtn" onclick="sendOnce()">Send one message</button>
                <button id="startBtn" onclick="startContinuous()">Start continuous generation</button>
                <button id="stopBtn" class="danger" onclick="stopContinuous()" disabled>Stop</button>
                <button class="secondary" onclick="resetLoadStats()">Reset counters</button>
            </div>
            <div class="stats-grid">
                <div class="stat-box">
                    <div class="stat-value" id="loadRequests">0</div>
                    <div class="stat-label">Generated</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="loadSuccess">0</div>
                    <div class="stat-label">Successful</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="loadErrors">0</div>
                    <div class="stat-label">Errors</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="loadAvgLatency">0.00</div>
                    <div class="stat-label">Client Avg Latency (ms)</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="loadSentMb">0.00</div>
                    <div class="stat-label">Sent Payload (MB)</div>
                </div>
                <div class="stat-box">
                    <div class="stat-value" id="loadMode">idle</div>
                    <div class="stat-label">Mode</div>
                </div>
            </div>
            <div class="preview-box">
                <label for="payloadPreview">Последняя сгенерированная полезная нагрузка</label>
                <pre id="payloadPreview">Пока ничего не отправлялось. Здесь будет тип данных, размер и начало в hex и в Base64.</pre>
            </div>
            </div>
        </div>

        <div class="card">
            <h2>📋 Activity Log</h2>
            <div class="log-container" id="log"></div>
        </div>
    </div>

    <script>
        (function () {
            const _fetch = window.fetch.bind(window);
            function authHeaders(base) {
                const h = new Headers(base || {});
                const token = localStorage.getItem('token');
                if (token && !h.has('Authorization')) {
                    h.set('Authorization', 'Bearer ' + token);
                }
                return h;
            }
            window.fetch = function (url, opts) {
                opts = opts || {};
                opts.credentials = 'same-origin';
                opts.headers = authHeaders(opts.headers);
                return _fetch(url, opts).then(function (resp) {
                    if (resp.status === 401) {
                        localStorage.removeItem('token');
                        window.location.replace('/login');
                    }
                    return resp;
                });
            };
            _fetch('/api/me', { credentials: 'same-origin', headers: authHeaders() })
                .then(function (r) { if (!r.ok) throw new Error('unauthorized'); return r.json(); })
                .catch(function () { window.location.replace('/login'); });
        })();
        const statusEl = document.getElementById('status');
        const logEl = document.getElementById('log');
        const loadStats = {
            requests: 0,
            success: 0,
            errors: 0,
            latencyMs: 0,
            sentBytes: 0
        };
        let continuousTimer = null;
        let continuousRunning = false;

        const videoDemo = {
            stream: null,
            ws: null,
            timer: null,
            pending: false
        };

        function setMainMode(mode) {
            const videoPanel = document.getElementById('panelVideo');
            const loadPanel = document.getElementById('panelLoad');
            const isVideo = mode === 'video';
            videoPanel.classList.toggle('active', isVideo);
            loadPanel.classList.toggle('active', !isVideo);
            document.querySelectorAll('input[name="mainMode"]').forEach(function (el) {
                el.checked = el.value === mode;
            });
            if (isVideo) {
                stopContinuous();
            } else {
                stopVideoDemo();
            }
        }

        function base64ToBytes(b64) {
            const bin = atob(b64);
            const out = new Uint8Array(bin.length);
            for (let i = 0; i < bin.length; i++) {
                out[i] = bin.charCodeAt(i);
            }
            return out;
        }

        function drawProcessedBase64(b64, w, h) {
            const rgb = base64ToBytes(b64);
            if (rgb.length < w * h * 3) {
                return;
            }
            const canvas = document.getElementById('camOutCanvas');
            const ctx = canvas.getContext('2d');
            const img = ctx.createImageData(w, h);
            for (let i = 0, j = 0; j < w * h * 3; i += 4, j += 3) {
                img.data[i] = rgb[j];
                img.data[i + 1] = rgb[j + 1];
                img.data[i + 2] = rgb[j + 2];
                img.data[i + 3] = 255;
            }
            ctx.putImageData(img, 0, 0);
        }

        function videoWsUrl() {
            const p = location.protocol === 'https:' ? 'wss:' : 'ws:';
            return p + '//' + location.host + '/ws';
        }

        function stopVideoDemo() {
            if (videoDemo.timer) {
                clearInterval(videoDemo.timer);
                videoDemo.timer = null;
            }
            videoDemo.pending = false;
            if (videoDemo.ws) {
                try {
                    videoDemo.ws.close();
                } catch (e) {}
                videoDemo.ws = null;
            }
            if (videoDemo.stream) {
                videoDemo.stream.getTracks().forEach(function (t) {
                    t.stop();
                });
                videoDemo.stream = null;
            }
            const v = document.getElementById('camVideo');
            v.srcObject = null;
            document.getElementById('videoStartBtn').disabled = false;
            document.getElementById('videoStopBtn').disabled = true;
            document.getElementById('videoStatus').textContent = 'Камера выключена.';
        }

        function videoSendFrame() {
            if (!videoDemo.ws || videoDemo.ws.readyState !== WebSocket.OPEN) {
                return;
            }
            if (videoDemo.pending) {
                return;
            }
            const v = document.getElementById('camVideo');
            if (v.readyState < 2) {
                return;
            }
            const cap = document.getElementById('camCapCanvas');
            const capCtx = cap.getContext('2d', { willReadFrequently: true });
            const W = cap.width;
            const H = cap.height;
            capCtx.drawImage(v, 0, 0, W, H);
            const rgba = capCtx.getImageData(0, 0, W, H).data;
            const rgb = new Uint8Array(W * H * 3);
            for (let i = 0, j = 0; i < rgba.length; i += 4, j += 3) {
                rgb[j] = rgba[i];
                rgb[j + 1] = rgba[i + 1];
                rgb[j + 2] = rgba[i + 2];
            }
            const payload = {
                data: bytesToBase64(rgb),
                width: W,
                height: H,
                filter: document.getElementById('filterVideo').value,
                blur_radius: 1.0
            };
            videoDemo.pending = true;
            try {
                videoDemo.ws.send(JSON.stringify(payload));
            } catch (e) {
                videoDemo.pending = false;
                log('WS send: ' + e.message, 'error');
            }
        }

        async function startVideoDemo() {
            const status = document.getElementById('videoStatus');
            stopVideoDemo();
            try {
                const stream = await navigator.mediaDevices.getUserMedia({
                    video: { width: { ideal: 640 }, height: { ideal: 480 } },
                    audio: false
                });
                videoDemo.stream = stream;
                const v = document.getElementById('camVideo');
                v.srcObject = stream;
                await v.play();
            } catch (e) {
                status.textContent = 'Нет доступа к камере: ' + e.message;
                log('Камера: ' + e.message, 'error');
                return;
            }

            const ws = new WebSocket(videoWsUrl());
            videoDemo.ws = ws;

            try {
                await new Promise(function (resolve, reject) {
                    const t = setTimeout(function () {
                        reject(new Error('таймаут подключения WS'));
                    }, 8000);
                    ws.onopen = function () {
                        clearTimeout(t);
                        resolve();
                    };
                    ws.onerror = function () {
                        clearTimeout(t);
                        reject(new Error('не удалось открыть WebSocket'));
                    };
                });
            } catch (e) {
                status.textContent = e.message;
                log('WS: ' + e.message, 'error');
                stopVideoDemo();
                return;
            }

            ws.onmessage = function (ev) {
                videoDemo.pending = false;
                let msg;
                try {
                    msg = JSON.parse(ev.data);
                } catch (err) {
                    log('WS: неверный JSON', 'error');
                    return;
                }
                if (msg.error) {
                    log('WS: ' + msg.error, 'error');
                    status.textContent = 'Ошибка: ' + msg.error;
                    return;
                }
                if (msg.data && msg.success) {
                    const cap = document.getElementById('camCapCanvas');
                    drawProcessedBase64(msg.data, cap.width, cap.height);
                    status.textContent = 'Кадр обработан за ' + (msg.processing_time_ms || 0).toFixed(2) + ' мс';
                } else if (msg.success === false) {
                    status.textContent = 'Процессор вернул success=false';
                    log('Кадр: success=false', 'error');
                }
            };

            ws.onerror = function () {
                log('WebSocket: ошибка соединения', 'error');
                status.textContent = 'Ошибка WebSocket';
            };

            ws.onclose = function () {
                videoDemo.pending = false;
            };

            const fps = Math.min(30, Math.max(1, Number(document.getElementById('videoFps').value) || 8));
            videoDemo.timer = setInterval(videoSendFrame, Math.max(33, Math.floor(1000 / fps)));
            document.getElementById('videoStartBtn').disabled = true;
            document.getElementById('videoStopBtn').disabled = false;
            status.textContent = 'Стрим активен (' + fps + ' к/с макс.).';
            log('Видео-режим: камера и WebSocket подключены', 'success');
        }
        
        function log(msg, type = '') {
            const entry = document.createElement('div');
            entry.className = 'log-entry ' + type;
            entry.textContent = new Date().toISOString().slice(11, 19) + ' ' + msg;
            logEl.appendChild(entry);
            logEl.scrollTop = logEl.scrollHeight;
        }

        function getLoadConfig() {
            const requestedSize = Math.max(3, Number(document.getElementById('messageSize').value) || 3);
            const width = Math.ceil(requestedSize / 3);
            const sentSize = width * 3;
            return {
                requestedSize,
                sentSize,
                width,
                height: 1,
                rps: Math.max(1, Number(document.getElementById('rps').value) || 1),
                filter: document.getElementById('filter').value
            };
        }

        function fillRandomBytes(bytes) {
            if (window.crypto && window.crypto.getRandomValues) {
                const chunkSize = 65536;
                for (let offset = 0; offset < bytes.length; offset += chunkSize) {
                    window.crypto.getRandomValues(bytes.subarray(offset, Math.min(offset + chunkSize, bytes.length)));
                }
            } else {
                for (let i = 0; i < bytes.length; i++) {
                    bytes[i] = Math.floor(Math.random() * 256);
                }
            }
        }

        function bytesToBase64(bytes) {
            let binary = '';
            const chunkSize = 8192;
            for (let offset = 0; offset < bytes.length; offset += chunkSize) {
                binary += String.fromCharCode.apply(null, bytes.subarray(offset, Math.min(offset + chunkSize, bytes.length)));
            }
            return btoa(binary);
        }

        function bytesToHexPrefix(bytes, maxBytes) {
            const n = Math.min(maxBytes, bytes.length);
            const parts = [];
            for (let i = 0; i < n; i++) {
                parts.push(bytes[i].toString(16).padStart(2, '0'));
                if ((i + 1) % 16 === 0) parts.push('\n');
                else if ((i + 1) % 4 === 0) parts.push(' ');
            }
            return parts.join('').trim();
        }

        function generateRandomPayload(size) {
            const bytes = new Uint8Array(size);
            fillRandomBytes(bytes);
            const base64 = bytesToBase64(bytes);
            return {
                bytes,
                base64,
                hexPrefix: bytesToHexPrefix(bytes, 48),
                base64Prefix: base64.slice(0, 80)
            };
        }

        function updatePayloadPreview(cfg, payloadInfo) {
            const el = document.getElementById('payloadPreview');
            const lines = [
                'Тип: псевдослучайные байты (crypto.getRandomValues / Math.random), не UTF-8 текст.',
                'Запрошено байт: ' + cfg.requestedSize + ', отправлено в теле: ' + cfg.sentSize + ' (кратно 3 для формата RGB).',
                'Как интерпретирует процессор: полоска RGB ' + cfg.width + '×' + cfg.height + '×3 байт.',
                'Фильтр: ' + cfg.filter,
                'Первые байты (hex, до 48):',
                payloadInfo.hexPrefix + (cfg.sentSize > 48 ? ' …' : ''),
                'Начало Base64 (до 80 символов):',
                payloadInfo.base64Prefix + (payloadInfo.base64.length > 80 ? ' …' : '')
            ];
            el.textContent = lines.join('\n');
        }

        function updateLoadStats() {
            const avg = loadStats.requests > 0 ? loadStats.latencyMs / loadStats.requests : 0;
            document.getElementById('loadRequests').textContent = loadStats.requests;
            document.getElementById('loadSuccess').textContent = loadStats.success;
            document.getElementById('loadErrors').textContent = loadStats.errors;
            document.getElementById('loadAvgLatency').textContent = avg.toFixed(2);
            document.getElementById('loadSentMb').textContent = (loadStats.sentBytes / 1024 / 1024).toFixed(2);
            document.getElementById('loadMode').textContent = continuousRunning ? 'running' : 'idle';
        }

        function resetLoadStats() {
            loadStats.requests = 0;
            loadStats.success = 0;
            loadStats.errors = 0;
            loadStats.latencyMs = 0;
            loadStats.sentBytes = 0;
            updateLoadStats();
            log('Load counters reset', 'info');
        }

        async function sendGeneratedMessage(silent = false) {
            const cfg = getLoadConfig();
            const payloadInfo = generateRandomPayload(cfg.sentSize);
            updatePayloadPreview(cfg, payloadInfo);
            const body = {
                data: payloadInfo.base64,
                width: cfg.width,
                height: cfg.height,
                filter: cfg.filter,
                blur_radius: 1.0
            };

            const start = performance.now();
            let ok = false;
            try {
                const resp = await fetch('/api/process', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify(body)
                });
                const elapsed = performance.now() - start;
                loadStats.requests++;
                loadStats.latencyMs += elapsed;
                loadStats.sentBytes += cfg.sentSize;

                if (resp.ok) {
                    const result = await resp.json();
                    ok = !!result.success;
                    if (ok) {
                        loadStats.success++;
                    } else {
                        loadStats.errors++;
                    }
                    if (!silent) {
                        log('Отправлено: ' + cfg.requestedSize + ' байт (фактически ' + cfg.sentSize + '), задержка процессора ' + (result.processing_time_ms || 0).toFixed(2) + ' мс', ok ? 'success' : 'error');
                    }
                } else {
                    loadStats.errors++;
                    const text = await resp.text();
                    if (!silent) {
                        log('Request failed: HTTP ' + resp.status + ' ' + text, 'error');
                    }
                }
            } catch (e) {
                loadStats.requests++;
                loadStats.errors++;
                if (!silent) {
                    log('Request failed: ' + e.message, 'error');
                }
            } finally {
                updateLoadStats();
            }
            return ok;
        }

        async function sendOnce() {
            document.getElementById('sendOnceBtn').disabled = true;
            await sendGeneratedMessage(false);
            document.getElementById('sendOnceBtn').disabled = false;
            updateStats();
        }

        function startContinuous() {
            if (continuousRunning) {
                return;
            }
            const cfg = getLoadConfig();
            continuousRunning = true;
            document.getElementById('startBtn').disabled = true;
            document.getElementById('stopBtn').disabled = false;
            updateLoadStats();
            log('Continuous generation started: ' + cfg.rps + ' rps, ' + cfg.requestedSize + ' bytes', 'info');

            const intervalMs = Math.max(10, Math.floor(1000 / cfg.rps));
            continuousTimer = setInterval(() => {
                if (!continuousRunning) {
                    return;
                }
                sendGeneratedMessage(true);
            }, intervalMs);
        }

        function stopContinuous() {
            continuousRunning = false;
            if (continuousTimer) {
                clearInterval(continuousTimer);
                continuousTimer = null;
            }
            document.getElementById('startBtn').disabled = false;
            document.getElementById('stopBtn').disabled = true;
            updateLoadStats();
            log('Continuous generation stopped', 'info');
            updateStats();
        }

        async function updateStats() {
            try {
                const resp = await fetch('/stats');
                if (resp.ok) {
                    const stats = await resp.json();
                    document.getElementById('fps').textContent = 
                        stats.framesPerSecond?.toFixed(1) || '-';
                    document.getElementById('latency').textContent = 
                        stats.avgProcessingTimeMs?.toFixed(2) || '-';
                    document.getElementById('processed').textContent = 
                        stats.totalFramesProcessed || '-';
                    document.getElementById('arena').textContent = 
                        ((stats.currentArenaSizeBytes || 0) / 1024 / 1024).toFixed(1);
                    statusEl.className = 'connected';
                }
            } catch (e) {
                statusEl.className = 'disconnected';
            }
        }

        async function checkHealth() {
            try {
                const resp = await fetch('/health');
                const health = await resp.json();
                if (health.status === 'healthy') {
                    log('Health check: OK', 'success');
                } else {
                    log('Health check: ' + health.status, 'error');
                }
            } catch (e) {
                log('Health check failed: ' + e.message, 'error');
            }
        }

        async function refreshRoutingAndProcessors() {
            const statusPre = document.getElementById('routingStatus');
            const box = document.getElementById('routingSwitch');
            try {
                const r = await fetch('/api/admin/routing');
                const j = await r.json();
                const addrs = j.addresses || [];
                const sticky = j.sticky_index;
                const health = j.health || {};
                let lines = 'Режим: ' + (j.mode || '?') + (sticky >= 0 ? ' (индекс ' + sticky + ')' : '') + '\n';
                addrs.forEach(function (a, i) {
                    const ok = health[a];
                    lines += '[' + i + '] ' + a + ' — ' + (ok === true ? 'OK' : ok === false ? 'down' : '?') + '\n';
                });
                statusPre.textContent = lines.trim();

                const curVal = sticky >= 0 ? String(sticky) : 'rr';
                box.innerHTML = '';
                const addRadio = function (value, label, checked) {
                    const lab = document.createElement('label');
                    const inp = document.createElement('input');
                    inp.type = 'radio';
                    inp.name = 'routingPick';
                    inp.value = value;
                    if (checked) inp.checked = true;
                    const span = document.createElement('span');
                    span.textContent = label;
                    lab.appendChild(inp);
                    lab.appendChild(span);
                    box.appendChild(lab);
                };
                addRadio('rr', 'Round-robin (все по очереди)', curVal === 'rr');
                addrs.forEach(function (a, i) {
                    addRadio(String(i), 'Только [' + i + '] ' + a, curVal === String(i));
                });
            } catch (e) {
                statusPre.textContent = 'Ошибка: ' + e.message;
            }

            const wrap = document.getElementById('processorsTableWrap');
            try {
                const r2 = await fetch('/api/admin/processors/stats');
                const rows = await r2.json();
                if (!Array.isArray(rows) || rows.length === 0) {
                    wrap.innerHTML = '<p class="hint">Нет процессоров в пуле.</p>';
                    return;
                }
                let html = '<table class="proc-admin-table"><thead><tr><th>#</th><th>Бэкенд</th><th>Адрес</th><th>Кадров всего</th><th>FPS</th><th>Память MB *</th><th>Peak MB *</th><th>Avg ms</th><th>Ошибка</th></tr></thead><tbody>';
                rows.forEach(function (row) {
                    const s = row.stats || {};
                    const arenaB = s.current_arena_size_bytes != null ? s.current_arena_size_bytes : s.currentArenaSizeBytes;
                    const peakB = s.peak_arena_size_bytes != null ? s.peak_arena_size_bytes : s.peakArenaSizeBytes;
                    const fps = s.frames_per_second != null ? s.frames_per_second : s.framesPerSecond;
                    const avgMs = s.avg_processing_time_ms != null ? s.avg_processing_time_ms : s.avgProcessingTimeMs;
                    const tot = s.total_frames_processed != null ? s.total_frames_processed : s.totalFramesProcessed;
                    const arenaMb = ((arenaB || 0) / 1024 / 1024).toFixed(2);
                    const peakMb = ((peakB || 0) / 1024 / 1024).toFixed(2);
                    const impl = (row.implementation === 'cpp') ? 'C++ (arena)' : 'Go (heap→proto)';
                    const tip = (row.implementation === 'cpp')
                        ? 'C++: реальная Memory Arena в полях protobuf'
                        : 'Go: current_arena_size_bytes = heapAlloc, peak = heapSys (имена полей исторические)';
                    html += '<tr><td>' + row.index + '</td><td title="' + tip.replace(/"/g, '') + '">' + impl + '</td><td>' + (row.address || '') + '</td><td>' + (tot ?? '') + '</td><td>' + (fps != null ? Number(fps).toFixed(2) : '') + '</td><td title="' + tip.replace(/"/g, '') + '">' + arenaMb + '</td><td title="' + tip.replace(/"/g, '') + '">' + peakMb + '</td><td>' + (avgMs != null ? Number(avgMs).toFixed(2) : '') + '</td><td>' + (row.error || '') + '</td></tr>';
                });
                html += '</tbody></table>';
                wrap.innerHTML = html;
            } catch (e) {
                wrap.innerHTML = '<p class="hint">Статистика: ' + e.message + '</p>';
            }
        }

        async function applyRouting() {
            const sel = document.querySelector('input[name="routingPick"]:checked');
            if (!sel) {
                log('Маршрут: не выбран вариант', 'error');
                return;
            }
            let body;
            if (sel.value === 'rr') {
                body = { mode: 'round_robin' };
            } else {
                body = { mode: 'fixed', sticky_index: parseInt(sel.value, 10) };
            }
            try {
                const resp = await fetch('/api/admin/routing', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body)
                });
                if (!resp.ok) {
                    const t = await resp.text();
                    log('Маршрут: ' + resp.status + ' ' + t, 'error');
                    return;
                }
                log('Маршрут обновлён: ' + JSON.stringify(body), 'success');
                await refreshRoutingAndProcessors();
                updateStats();
            } catch (e) {
                log('Маршрут: ' + e.message, 'error');
            }
        }

        log('Gateway service loaded', 'info');
        updateLoadStats();
        checkHealth();
        updateStats();
        refreshRoutingAndProcessors();
        setInterval(updateStats, 2000);
        setInterval(refreshRoutingAndProcessors, 4000);
    </script>
</body>
</html>`
