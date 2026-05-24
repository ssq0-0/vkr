#include <iostream>
#include <csignal>
#include <cstdlib>
#include <string>

#include "server/grpc_server.hpp"
#include "server/metrics_server.hpp"

namespace {
    server::GrpcServer* g_server = nullptr;
    server::MetricsServer* g_metrics = nullptr;
    
    void signal_handler(int signal) {
        std::cout << "\nReceived signal " << signal << ", shutting down..." << std::endl;
        if (g_metrics) {
            g_metrics->stop();
        }
        if (g_server) {
            g_server->stop();
        }
    }
}

void print_usage(const char* program) {
    std::cout << "Usage: " << program << " [options]\n"
              << "Options:\n"
              << "  --host <host>    Host to bind (default: 0.0.0.0)\n"
              << "  --port <port>    Port to listen (default: 9090)\n"
              << "  --metrics-port <port>  Metrics HTTP port (default: 9091)\n"
              << "  --help           Show this help\n";
}

int main(int argc, char* argv[]) {
    std::string host = "0.0.0.0";
    int port = 9090;
    int metrics_port = 9091;
    
    // Парсинг аргументов командной строки
    for (int i = 1; i < argc; ++i) {
        std::string arg = argv[i];
        
        if (arg == "--host" && i + 1 < argc) {
            host = argv[++i];
        } else if (arg == "--port" && i + 1 < argc) {
            port = std::atoi(argv[++i]);
        } else if (arg == "--metrics-port" && i + 1 < argc) {
            metrics_port = std::atoi(argv[++i]);
        } else if (arg == "--help") {
            print_usage(argv[0]);
            return 0;
        }
    }

    if (const char* env_metrics_port = std::getenv("METRICS_PORT")) {
        metrics_port = std::atoi(env_metrics_port);
    }
    
    // Настройка обработчиков сигналов
    std::signal(SIGINT, signal_handler);
    std::signal(SIGTERM, signal_handler);
    
    std::cout << "=== Streaming Processor Service (C++ Arena / No Arena) ===" << std::endl;
    std::cout << "Starting server on " << host << ":" << port << std::endl;
    
    try {
        server::GrpcServer server(host, port);
        g_server = &server;
        
        server.start();

        server::MetricsServer metrics(server.stats(), metrics_port);
        g_metrics = &metrics;
        metrics.start();

        server.wait();
        
        metrics.stop();
        g_server = nullptr;
        g_metrics = nullptr;
        
    } catch (const std::exception& e) {
        std::cerr << "Error: " << e.what() << std::endl;
        return 1;
    }
    
    std::cout << "Server stopped gracefully." << std::endl;
    return 0;
}


