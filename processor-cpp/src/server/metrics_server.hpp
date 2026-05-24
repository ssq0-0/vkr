#pragma once

#include <atomic>
#include <chrono>
#include <string>
#include <thread>

#include "grpc_server.hpp"

namespace server {

class MetricsServer {
public:
    MetricsServer(const ServerStats& stats, int port);
    ~MetricsServer();

    void start();
    void stop();

private:
    void serve();
    std::string build_metrics() const;

    const ServerStats& stats_;
    int port_;
    std::atomic<bool> running_{false};
    std::thread thread_;
    int server_fd_{-1};
    std::chrono::steady_clock::time_point start_time_;
};

} // namespace server
