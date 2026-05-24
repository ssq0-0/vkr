#pragma once

#include <memory>
#include <string>
#include <atomic>
#include <thread>
#include <vector>
#include <queue>
#include <mutex>
#include <condition_variable>
#include <functional>
#include <vector>

#include <grpcpp/grpcpp.h>
#include "streaming.grpc.pb.h"
#include "../arena/memory_arena.hpp"
#include "../processing/image_processor.hpp"

namespace server {

/**
 * Thread Pool для параллельной обработки
 */
class ThreadPool {
public:
    explicit ThreadPool(size_t num_threads);
    ~ThreadPool();
    
    template<typename F>
    void enqueue(F&& task) {
        {
            std::unique_lock<std::mutex> lock(mutex_);
            tasks_.push(std::forward<F>(task));
        }
        cv_.notify_one();
    }
    
    void shutdown();
    size_t size() const { return workers_.size(); }

private:
    std::vector<std::thread> workers_;
    std::queue<std::function<void()>> tasks_;
    std::mutex mutex_;
    std::condition_variable cv_;
    std::atomic<bool> stop_{false};
};

/**
 * Статистика сервера
 */
struct ServerStats {
    std::atomic<int64_t> total_frames_processed{0};
    std::atomic<int64_t> total_processing_time_ns{0};
    std::atomic<int64_t> current_arena_size_bytes{0};
    std::atomic<int64_t> peak_arena_size_bytes{0};
    std::atomic<int64_t> total_arena_allocations{0};
    std::atomic<int64_t> total_arena_resets{0};
    std::atomic<int64_t> total_bytes_allocated{0};
    std::atomic<int64_t> active_arenas{0};
    std::atomic<int64_t> total_heap_allocations{0};
    std::atomic<int64_t> total_heap_bytes_allocated{0};
    std::atomic<int64_t> arena_mode_frames{0};
    std::atomic<int64_t> heap_mode_frames{0};
    
    double avg_processing_time_ms() const {
        if (total_frames_processed == 0) return 0;
        return static_cast<double>(total_processing_time_ns.load()) / 
               total_frames_processed.load() / 1000000.0;
    }
    
    double frames_per_second() const;
};

/**
 * gRPC сервис для обработки медиа-потока
 */
class StreamingProcessorServiceImpl final : public streaming::StreamingProcessor::Service {
public:
    StreamingProcessorServiceImpl();
    ~StreamingProcessorServiceImpl();
    
    grpc::Status ProcessMediaStream(
        grpc::ServerContext* context,
        grpc::ServerReaderWriter<streaming::ProcessedChunk, streaming::MediaChunk>* stream) override;
    
    grpc::Status GetStats(
        grpc::ServerContext* context,
        const streaming::StatsRequest* request,
        streaming::StatsResponse* response) override;
    
    const ServerStats& stats() const { return stats_; }

private:
    streaming::ProcessedChunk process_chunk_arena(const streaming::MediaChunk& chunk, 
                                                  arena::MemoryArena& arena);
    streaming::ProcessedChunk process_chunk_heap(const streaming::MediaChunk& chunk);
    void apply_filter_heap(const streaming::MediaChunk& chunk, const uint8_t* input,
                           std::vector<uint8_t>& output);
    
    ThreadPool thread_pool_;
    ServerStats stats_;
    std::chrono::steady_clock::time_point start_time_;
};

/**
 * gRPC сервер
 */
class GrpcServer {
public:
    GrpcServer(const std::string& address, int port);
    ~GrpcServer();
    
    void start();
    void stop();
    void wait();
    
    const ServerStats& stats() const { return service_->stats(); }

private:
    std::string address_;
    int port_;
    std::unique_ptr<grpc::Server> server_;
    std::unique_ptr<StreamingProcessorServiceImpl> service_;
};

} // namespace server


