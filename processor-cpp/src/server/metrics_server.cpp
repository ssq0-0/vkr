#include "metrics_server.hpp"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

#include <cstddef>
#include <cstring>
#include <iostream>
#include <sstream>
#include <string_view>

namespace {

// send() может вернуть меньше запрошенного; клиент ждёт остаток тела по Content-Length — «вечная загрузка».
bool send_all(int fd, const void* data, std::size_t len) {
    const auto* p = static_cast<const char*>(data);
    std::size_t off = 0;
    while (off < len) {
        const ssize_t n = ::send(fd, p + off, len - off, 0);
        if (n <= 0) {
            return false;
        }
        off += static_cast<std::size_t>(n);
    }
    return true;
}

} // namespace

namespace server {

MetricsServer::MetricsServer(const ServerStats& stats, int port)
    : stats_(stats), port_(port), start_time_(std::chrono::steady_clock::now()) {}

MetricsServer::~MetricsServer() {
    stop();
}

void MetricsServer::start() {
    if (running_.exchange(true)) {
        return;
    }
    thread_ = std::thread(&MetricsServer::serve, this);
}

void MetricsServer::stop() {
    if (!running_.exchange(false)) {
        return;
    }
    if (server_fd_ >= 0) {
        shutdown(server_fd_, SHUT_RDWR);
        close(server_fd_);
        server_fd_ = -1;
    }
    if (thread_.joinable()) {
        thread_.join();
    }
}

std::string MetricsServer::build_metrics() const {
    const auto frames = stats_.total_frames_processed.load();
    const auto total_time_ns = stats_.total_processing_time_ns.load();
    const auto current_arena = stats_.current_arena_size_bytes.load();
    const auto peak_arena = stats_.peak_arena_size_bytes.load();
    const auto total_allocs = stats_.total_arena_allocations.load();
    const auto total_resets = stats_.total_arena_resets.load();
    const auto active_arenas = stats_.active_arenas.load();
    const auto heap_allocs = stats_.total_heap_allocations.load();
    const auto heap_bytes = stats_.total_heap_bytes_allocated.load();
    const auto arena_frames = stats_.arena_mode_frames.load();
    const auto heap_frames = stats_.heap_mode_frames.load();

    const auto now = std::chrono::steady_clock::now();
    const auto elapsed_seconds =
        std::chrono::duration_cast<std::chrono::duration<double>>(now - start_time_).count();
    const double fps = elapsed_seconds > 0.0 ? static_cast<double>(frames) / elapsed_seconds : 0.0;
    const double avg_ms = frames > 0 ? static_cast<double>(total_time_ns) / frames / 1e6 : 0.0;

    std::ostringstream out;
    out << "# HELP processor_cpp_frames_processed_total Total number of frames processed\n";
    out << "# TYPE processor_cpp_frames_processed_total counter\n";
    out << "processor_cpp_frames_processed_total " << frames << "\n";

    out << "# HELP processor_cpp_processing_time_ns_total Total processing time in nanoseconds\n";
    out << "# TYPE processor_cpp_processing_time_ns_total counter\n";
    out << "processor_cpp_processing_time_ns_total " << total_time_ns << "\n";

    out << "# HELP processor_cpp_avg_processing_time_ms Average processing time in milliseconds\n";
    out << "# TYPE processor_cpp_avg_processing_time_ms gauge\n";
    out << "processor_cpp_avg_processing_time_ms " << avg_ms << "\n";

    out << "# HELP processor_cpp_frames_per_second Frames per second since process start\n";
    out << "# TYPE processor_cpp_frames_per_second gauge\n";
    out << "processor_cpp_frames_per_second " << fps << "\n";

    out << "# HELP processor_cpp_current_arena_size_bytes Current arena size in bytes\n";
    out << "# TYPE processor_cpp_current_arena_size_bytes gauge\n";
    out << "processor_cpp_current_arena_size_bytes " << current_arena << "\n";

    out << "# HELP processor_cpp_peak_arena_size_bytes Peak arena size in bytes\n";
    out << "# TYPE processor_cpp_peak_arena_size_bytes gauge\n";
    out << "processor_cpp_peak_arena_size_bytes " << peak_arena << "\n";

    out << "# HELP processor_cpp_total_arena_allocations Total arena allocations\n";
    out << "# TYPE processor_cpp_total_arena_allocations counter\n";
    out << "processor_cpp_total_arena_allocations " << total_allocs << "\n";

    out << "# HELP processor_cpp_total_arena_resets Total arena resets\n";
    out << "# TYPE processor_cpp_total_arena_resets counter\n";
    out << "processor_cpp_total_arena_resets " << total_resets << "\n";

    out << "# HELP processor_cpp_active_arenas Number of active arenas\n";
    out << "# TYPE processor_cpp_active_arenas gauge\n";
    out << "processor_cpp_active_arenas " << active_arenas << "\n";

    out << "# HELP processor_cpp_heap_allocations_total Total heap allocations in no_arena mode\n";
    out << "# TYPE processor_cpp_heap_allocations_total counter\n";
    out << "processor_cpp_heap_allocations_total " << heap_allocs << "\n";

    out << "# HELP processor_cpp_heap_bytes_allocated_total Total bytes allocated in no_arena mode\n";
    out << "# TYPE processor_cpp_heap_bytes_allocated_total counter\n";
    out << "processor_cpp_heap_bytes_allocated_total " << heap_bytes << "\n";

    out << "# HELP processor_cpp_arena_mode_frames_total Frames processed in arena mode\n";
    out << "# TYPE processor_cpp_arena_mode_frames_total counter\n";
    out << "processor_cpp_arena_mode_frames_total " << arena_frames << "\n";

    out << "# HELP processor_cpp_heap_mode_frames_total Frames processed in no_arena mode\n";
    out << "# TYPE processor_cpp_heap_mode_frames_total counter\n";
    out << "processor_cpp_heap_mode_frames_total " << heap_frames << "\n";

    return out.str();
}

void MetricsServer::serve() {
    server_fd_ = socket(AF_INET, SOCK_STREAM, 0);
    if (server_fd_ < 0) {
        std::cerr << "Metrics server: failed to create socket\n";
        running_ = false;
        return;
    }

    int opt = 1;
    setsockopt(server_fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons(static_cast<uint16_t>(port_));

    if (bind(server_fd_, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::cerr << "Metrics server: failed to bind on port " << port_ << "\n";
        running_ = false;
        close(server_fd_);
        server_fd_ = -1;
        return;
    }

    if (listen(server_fd_, 16) < 0) {
        std::cerr << "Metrics server: failed to listen\n";
        running_ = false;
        close(server_fd_);
        server_fd_ = -1;
        return;
    }

    while (running_) {
        sockaddr_in client_addr{};
        socklen_t client_len = sizeof(client_addr);
        int client_fd = accept(server_fd_, reinterpret_cast<sockaddr*>(&client_addr), &client_len);
        if (client_fd < 0) {
            if (!running_) {
                break;
            }
            continue;
        }

        char buffer[1024];
        ssize_t bytes = recv(client_fd, buffer, sizeof(buffer) - 1, 0);
        if (bytes <= 0) {
            close(client_fd);
            continue;
        }
        buffer[bytes] = '\0';

        // Первая строка запроса (браузер: "GET /metrics HTTP/1.1", Prometheus — то же).
        std::string_view req(buffer, static_cast<size_t>(bytes));
        const auto line_end = req.find("\r\n");
        if (line_end != std::string_view::npos) {
            req = req.substr(0, line_end);
        }
        const bool is_metrics =
            req.size() >= 12 && req.substr(0, 12) == "GET /metrics" &&
            (req.size() == 12 || req[12] == ' ' || req[12] == '?' || req[12] == '/');
        if (is_metrics) {
            const std::string body = build_metrics();
            std::ostringstream resp;
            resp << "HTTP/1.1 200 OK\r\n"
                 << "Content-Type: text/plain; version=0.0.4\r\n"
                 << "Content-Length: " << body.size() << "\r\n"
                 << "Connection: close\r\n\r\n"
                 << body;
            const std::string out = resp.str();
            if (!send_all(client_fd, out.data(), out.size())) {
                std::cerr << "Metrics server: short send\n";
            }
        } else {
            const char* resp =
                "HTTP/1.1 404 Not Found\r\n"
                "Content-Length: 0\r\n"
                "Connection: close\r\n\r\n";
            const size_t resp_len = std::strlen(resp);
            if (!send_all(client_fd, resp, resp_len)) {
                std::cerr << "Metrics server: short send (404)\n";
            }
        }

        close(client_fd);
    }
}

} // namespace server
