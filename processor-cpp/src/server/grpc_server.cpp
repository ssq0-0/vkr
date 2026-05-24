#include "grpc_server.hpp"
#include <iostream>
#include <chrono>
#include <algorithm>
#include <cmath>
#include <string>
#include <vector>

namespace server {

// ThreadPool implementation

ThreadPool::ThreadPool(size_t num_threads) {
    for (size_t i = 0; i < num_threads; ++i) {
        workers_.emplace_back([this] {
            while (true) {
                std::function<void()> task;
                {
                    std::unique_lock<std::mutex> lock(mutex_);
                    cv_.wait(lock, [this] { return stop_ || !tasks_.empty(); });
                    
                    if (stop_ && tasks_.empty()) return;
                    
                    task = std::move(tasks_.front());
                    tasks_.pop();
                }
                task();
            }
        });
    }
}

ThreadPool::~ThreadPool() {
    shutdown();
}

void ThreadPool::shutdown() {
    stop_ = true;
    cv_.notify_all();
    for (auto& worker : workers_) {
        if (worker.joinable()) {
            worker.join();
        }
    }
}

// ServerStats implementation

double ServerStats::frames_per_second() const {
    // Простая реализация - можно улучшить с временным окном
    return 0;  // Будет вычисляться из внешних метрик
}

// StreamingProcessorServiceImpl implementation

StreamingProcessorServiceImpl::StreamingProcessorServiceImpl()
    : thread_pool_(std::thread::hardware_concurrency())
    , start_time_(std::chrono::steady_clock::now()) {
    std::cout << "StreamingProcessorService initialized with " 
              << std::thread::hardware_concurrency() << " threads" << std::endl;
}

StreamingProcessorServiceImpl::~StreamingProcessorServiceImpl() = default;

grpc::Status StreamingProcessorServiceImpl::ProcessMediaStream(
    grpc::ServerContext* context,
    grpc::ServerReaderWriter<streaming::ProcessedChunk, streaming::MediaChunk>* stream) {
    
    std::string memory_mode = "arena";
    const auto& metadata = context->client_metadata();
    auto mode_it = metadata.find("memory-mode");
    if (mode_it != metadata.end()) {
        memory_mode = std::string(mode_it->second.data(), mode_it->second.length());
    }
    const bool use_arena = memory_mode != "heap" && memory_mode != "no_arena";

    // Создаем арену для этого потока. В heap-режиме она не используется, но остаётся локальной,
    // чтобы не менять сигнатуру потокового обработчика и статистику жизненного цикла.
    arena::MemoryArena arena(8 * 1024 * 1024);  // 8 MB
    if (use_arena) {
        stats_.active_arenas++;
    }
    
    streaming::MediaChunk chunk;
    int frames_in_batch = 0;
    const int RESET_INTERVAL = 10;  // Сбрасываем арену каждые 10 кадров (чаще для демо)
    
    while (stream->Read(&chunk)) {
        // Проверяем, не отменен ли контекст
        if (context->IsCancelled()) {
            if (use_arena) {
                stats_.total_arena_resets++;  // Арена будет освобождена
                stats_.active_arenas--;
            }
            return grpc::Status(grpc::StatusCode::CANCELLED, "Client cancelled");
        }
        
        // Обрабатываем чанк
        auto result = use_arena ? process_chunk_arena(chunk, arena) : process_chunk_heap(chunk);
        
        if (use_arena) {
            // Обновляем статистику арены после каждого кадра
            stats_.current_arena_size_bytes = static_cast<int64_t>(arena.used_bytes());
            
            int64_t peak = stats_.peak_arena_size_bytes.load();
            int64_t current = static_cast<int64_t>(arena.peak_usage());
            while (current > peak && 
                   !stats_.peak_arena_size_bytes.compare_exchange_weak(peak, current)) {}
        }
        
        // Отправляем результат
        if (!stream->Write(result)) {
            if (use_arena) {
                stats_.total_arena_resets++;  // Арена будет освобождена
                stats_.active_arenas--;
            }
            return grpc::Status(grpc::StatusCode::UNKNOWN, "Failed to write response");
        }
        
        // Периодически сбрасываем арену для переиспользования памяти
        frames_in_batch++;
        if (use_arena && frames_in_batch >= RESET_INTERVAL) {
            stats_.total_arena_resets++;
            arena.reset();
            frames_in_batch = 0;
        }
    }
    
    // Одиночный кадр (как у шлюза: один Send — один Read) или хвост батча: сбрасываем локальную арену.
    // Не обнуляем stats_.current_arena_size_bytes — иначе GetStats почти всегда видит 0 между запросами,
    // хотя кадры обрабатываются. Текущее «живое» used уже записано в stats_ после process_chunk (см. цикл выше).
    if (use_arena && frames_in_batch > 0 && frames_in_batch < RESET_INTERVAL) {
        stats_.total_arena_resets++;
        arena.reset();
    }
    
    // Арена освобождается при выходе из функции
    if (use_arena) {
        stats_.active_arenas--;
    }
    return grpc::Status::OK;
}

streaming::ProcessedChunk StreamingProcessorServiceImpl::process_chunk_arena(
    const streaming::MediaChunk& chunk, arena::MemoryArena& arena) {
    
    auto start = std::chrono::high_resolution_clock::now();
    
    streaming::ProcessedChunk result;
    result.set_sequence_id(chunk.sequence_id());
    
    try {
        // Проверяем тип данных
        if (chunk.type() != streaming::MEDIA_TYPE_VIDEO && 
            chunk.type() != streaming::MEDIA_TYPE_IMAGE) {
            result.set_success(false);
            result.set_error_message("Unsupported media type");
            return result;
        }
        
        int width = chunk.width();
        int height = chunk.height();
        size_t expected_size = width * height * 3;  // RGB
        
        if (chunk.data().size() != expected_size) {
            result.set_success(false);
            result.set_error_message("Invalid data size");
            return result;
        }
        
        // Создаем процессор изображений
        processing::ImageProcessor processor(arena);
        
        // Получаем параметры обработки
        float blur_radius = 5.0f;
        if (chunk.has_params() && chunk.params().blur_radius() > 0) {
            blur_radius = chunk.params().blur_radius();
        }
        
        // Обрабатываем в зависимости от типа фильтра
        const uint8_t* input = reinterpret_cast<const uint8_t*>(chunk.data().data());
        uint8_t* output = nullptr;
        
        streaming::FilterType filter = streaming::FILTER_TYPE_GAUSSIAN_BLUR;
        if (chunk.has_params()) {
            filter = chunk.params().filter();
        }
        
        // Выделяем память для выхода из арены
        output = arena.allocate_array<uint8_t>(expected_size);
        
        // Обновляем статистику аллокаций
        stats_.total_arena_allocations++;
        stats_.total_bytes_allocated += expected_size;
        
        processing::Image img_in = {const_cast<uint8_t*>(input), width, height, 3};
        processing::Image img_out = {output, width, height, 3};
        
        switch (filter) {
            case streaming::FILTER_TYPE_GAUSSIAN_BLUR:
                processor.gaussian_blur(img_in, img_out, blur_radius);
                break;
            case streaming::FILTER_TYPE_SHARPEN:
                processor.sharpen(img_in, img_out, 
                    chunk.has_params() ? chunk.params().intensity() : 1.0f);
                break;
            case streaming::FILTER_TYPE_EDGE_DETECT:
                processor.edge_detect(img_in, img_out);
                break;
            case streaming::FILTER_TYPE_GRAYSCALE:
                processor.grayscale(img_in, img_out);
                break;
            default:
                processor.gaussian_blur(img_in, img_out, blur_radius);
        }
        
        // Копируем результат в ответ
        result.set_data(output, expected_size);
        result.set_success(true);
        
        // Обновляем статистику
        stats_.total_frames_processed++;
        stats_.arena_mode_frames++;
        
    } catch (const std::exception& e) {
        result.set_success(false);
        result.set_error_message(e.what());
    }
    
    auto end = std::chrono::high_resolution_clock::now();
    auto duration = std::chrono::duration_cast<std::chrono::nanoseconds>(end - start);
    
    result.set_processing_time_ns(duration.count());
    stats_.total_processing_time_ns += duration.count();
    
    // Заполняем метрики памяти
    auto* mem_metrics = result.mutable_memory_metrics();
    mem_metrics->set_arena_allocated_bytes(arena.allocated_bytes());
    mem_metrics->set_arena_used_bytes(arena.used_bytes());
    mem_metrics->set_total_allocations(stats_.total_arena_allocations);
    
    return result;
}

streaming::ProcessedChunk StreamingProcessorServiceImpl::process_chunk_heap(
    const streaming::MediaChunk& chunk) {
    
    auto start = std::chrono::high_resolution_clock::now();
    
    streaming::ProcessedChunk result;
    result.set_sequence_id(chunk.sequence_id());
    
    try {
        if (chunk.type() != streaming::MEDIA_TYPE_VIDEO && 
            chunk.type() != streaming::MEDIA_TYPE_IMAGE) {
            result.set_success(false);
            result.set_error_message("Unsupported media type");
            return result;
        }
        
        int width = chunk.width();
        int height = chunk.height();
        size_t expected_size = width * height * 3;
        
        if (chunk.data().size() != expected_size) {
            result.set_success(false);
            result.set_error_message("Invalid data size");
            return result;
        }
        
        const uint8_t* input = reinterpret_cast<const uint8_t*>(chunk.data().data());
        std::vector<uint8_t> output(expected_size);
        stats_.total_heap_allocations++;
        stats_.total_heap_bytes_allocated += static_cast<int64_t>(expected_size);
        
        apply_filter_heap(chunk, input, output);
        
        result.set_data(output.data(), expected_size);
        result.set_success(true);
        stats_.total_frames_processed++;
        stats_.heap_mode_frames++;
        
    } catch (const std::exception& e) {
        result.set_success(false);
        result.set_error_message(e.what());
    }
    
    auto end = std::chrono::high_resolution_clock::now();
    auto duration = std::chrono::duration_cast<std::chrono::nanoseconds>(end - start);
    
    result.set_processing_time_ns(duration.count());
    stats_.total_processing_time_ns += duration.count();
    
    auto* mem_metrics = result.mutable_memory_metrics();
    mem_metrics->set_arena_allocated_bytes(0);
    mem_metrics->set_arena_used_bytes(0);
    mem_metrics->set_total_allocations(stats_.total_heap_allocations);
    mem_metrics->set_heap_used_bytes(stats_.total_heap_bytes_allocated);
    
    return result;
}

void StreamingProcessorServiceImpl::apply_filter_heap(
    const streaming::MediaChunk& chunk, const uint8_t* input, std::vector<uint8_t>& output) {
    
    const int width = chunk.width();
    const int height = chunk.height();
    const int channels = 3;
    streaming::FilterType filter = streaming::FILTER_TYPE_GAUSSIAN_BLUR;
    if (chunk.has_params()) {
        filter = chunk.params().filter();
    }
    
    if (filter == streaming::FILTER_TYPE_GRAYSCALE) {
        for (int i = 0; i < width * height; ++i) {
            int idx = i * 3;
            uint8_t gray = static_cast<uint8_t>(
                0.299f * input[idx] + 0.587f * input[idx + 1] + 0.114f * input[idx + 2]);
            output[idx] = gray;
            output[idx + 1] = gray;
            output[idx + 2] = gray;
        }
        return;
    }
    
    if (filter == streaming::FILTER_TYPE_SHARPEN) {
        float intensity = chunk.has_params() ? chunk.params().intensity() : 1.0f;
        float kernel[9] = {
            0, -intensity, 0,
            -intensity, 1 + 4 * intensity, -intensity,
            0, -intensity, 0
        };
        std::copy(input, input + output.size(), output.begin());
        for (int y = 1; y < height - 1; ++y) {
            for (int x = 1; x < width - 1; ++x) {
                for (int c = 0; c < channels; ++c) {
                    float sum = 0;
                    for (int ky = -1; ky <= 1; ++ky) {
                        for (int kx = -1; kx <= 1; ++kx) {
                            int idx = ((y + ky) * width + (x + kx)) * channels + c;
                            sum += input[idx] * kernel[(ky + 1) * 3 + (kx + 1)];
                        }
                    }
                    int out_idx = (y * width + x) * channels + c;
                    output[out_idx] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum)));
                }
            }
        }
        return;
    }
    
    if (filter == streaming::FILTER_TYPE_EDGE_DETECT) {
        int sobel_x[9] = {-1, 0, 1, -2, 0, 2, -1, 0, 1};
        int sobel_y[9] = {-1, -2, -1, 0, 0, 0, 1, 2, 1};
        std::vector<uint8_t> gray(width * height);
        stats_.total_heap_allocations++;
        stats_.total_heap_bytes_allocated += static_cast<int64_t>(gray.size());
        for (int i = 0; i < width * height; ++i) {
            int idx = i * 3;
            gray[i] = static_cast<uint8_t>(
                0.299f * input[idx] + 0.587f * input[idx + 1] + 0.114f * input[idx + 2]);
        }
        std::fill(output.begin(), output.end(), 0);
        for (int y = 1; y < height - 1; ++y) {
            for (int x = 1; x < width - 1; ++x) {
                int gx = 0, gy = 0;
                for (int ky = -1; ky <= 1; ++ky) {
                    for (int kx = -1; kx <= 1; ++kx) {
                        int pixel = gray[(y + ky) * width + (x + kx)];
                        gx += pixel * sobel_x[(ky + 1) * 3 + (kx + 1)];
                        gy += pixel * sobel_y[(ky + 1) * 3 + (kx + 1)];
                    }
                }
                int magnitude = std::min(255, static_cast<int>(std::sqrt(gx * gx + gy * gy)));
                int out_idx = (y * width + x) * 3;
                output[out_idx] = magnitude;
                output[out_idx + 1] = magnitude;
                output[out_idx + 2] = magnitude;
            }
        }
        return;
    }
    
    float radius = 5.0f;
    if (chunk.has_params() && chunk.params().blur_radius() > 0) {
        radius = chunk.params().blur_radius();
    }
    int kernel_size = static_cast<int>(std::ceil(radius * 6)) | 1;
    kernel_size = std::max(3, std::min(kernel_size, 31));
    int half = kernel_size / 2;
    float sigma2 = 2.0f * radius * radius;
    std::vector<float> kernel(kernel_size);
    std::vector<float> temp(width * height * channels);
    stats_.total_heap_allocations += 2;
    stats_.total_heap_bytes_allocated += static_cast<int64_t>(
        kernel.size() * sizeof(float) + temp.size() * sizeof(float));
    float sum = 0.0f;
    for (int i = 0; i < kernel_size; ++i) {
        float x = static_cast<float>(i - half);
        kernel[i] = std::exp(-(x * x) / sigma2);
        sum += kernel[i];
    }
    for (float& v : kernel) {
        v /= sum;
    }
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            for (int c = 0; c < channels; ++c) {
                float acc = 0;
                for (int k = 0; k < kernel_size; ++k) {
                    int px = std::max(0, std::min(x - half + k, width - 1));
                    acc += input[(y * width + px) * channels + c] * kernel[k];
                }
                temp[(y * width + x) * channels + c] = acc;
            }
        }
    }
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            for (int c = 0; c < channels; ++c) {
                float acc = 0;
                for (int k = 0; k < kernel_size; ++k) {
                    int py = std::max(0, std::min(y - half + k, height - 1));
                    acc += temp[(py * width + x) * channels + c] * kernel[k];
                }
                output[(y * width + x) * channels + c] =
                    static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, acc)));
            }
        }
    }
}

grpc::Status StreamingProcessorServiceImpl::GetStats(
    grpc::ServerContext* context,
    const streaming::StatsRequest* request,
    streaming::StatsResponse* response) {
    
    response->set_total_frames_processed(stats_.total_frames_processed);
    response->set_total_processing_time_ns(stats_.total_processing_time_ns);
    response->set_avg_processing_time_ms(stats_.avg_processing_time_ms());
    response->set_current_arena_size_bytes(stats_.current_arena_size_bytes);
    response->set_peak_arena_size_bytes(stats_.peak_arena_size_bytes);
    response->set_total_arena_allocations(stats_.total_arena_allocations);
    response->set_total_arena_resets(stats_.total_arena_resets);
    
    auto now = std::chrono::steady_clock::now();
    auto elapsed = std::chrono::duration_cast<std::chrono::seconds>(now - start_time_).count();
    if (elapsed > 0) {
        response->set_frames_per_second(
            static_cast<double>(stats_.total_frames_processed) / elapsed);
    } else {
        response->set_frames_per_second(0);
    }
    
    // Добавим отладочный вывод
    std::cout << "[Stats] Frames: " << stats_.total_frames_processed 
              << ", Arena: " << stats_.current_arena_size_bytes << " bytes"
              << ", Peak: " << stats_.peak_arena_size_bytes << " bytes"
              << ", Allocs: " << stats_.total_arena_allocations 
              << ", Resets: " << stats_.total_arena_resets
              << ", Active: " << stats_.active_arenas << std::endl;
    
    return grpc::Status::OK;
}

// GrpcServer implementation

GrpcServer::GrpcServer(const std::string& address, int port)
    : address_(address), port_(port) {}

GrpcServer::~GrpcServer() {
    stop();
}

void GrpcServer::start() {
    std::string server_address = address_ + ":" + std::to_string(port_);
    
    service_ = std::make_unique<StreamingProcessorServiceImpl>();
    
    grpc::ServerBuilder builder;
    builder.AddListeningPort(server_address, grpc::InsecureServerCredentials());
    builder.RegisterService(service_.get());
    
    // Настройки производительности
    builder.SetMaxReceiveMessageSize(100 * 1024 * 1024);  // 100 MB
    builder.SetMaxSendMessageSize(100 * 1024 * 1024);
    
    server_ = builder.BuildAndStart();
    
    std::cout << "Server listening on " << server_address << std::endl;
}

void GrpcServer::stop() {
    if (server_) {
        server_->Shutdown();
    }
}

void GrpcServer::wait() {
    if (server_) {
        server_->Wait();
    }
}

} // namespace server


