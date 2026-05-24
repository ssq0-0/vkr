#include <iostream>
#include <chrono>
#include <vector>
#include <random>
#include <iomanip>
#include <cstring>
#include "../src/arena/memory_arena.hpp"
#include "../src/processing/image_processor.hpp"

using namespace arena;
using namespace processing;
using namespace std::chrono;

// Размеры тестового кадра
constexpr int WIDTH = 640;
constexpr int HEIGHT = 480;
constexpr int CHANNELS = 3;
constexpr size_t FRAME_SIZE = WIDTH * HEIGHT * CHANNELS;

struct BenchmarkResult {
    std::string name;
    double total_time_ms;
    int iterations;
    double avg_time_ms;
    double throughput_fps;
    size_t total_allocations;
    size_t peak_memory_bytes;
};

void print_result(const BenchmarkResult& r) {
    std::cout << std::setw(35) << std::left << r.name
              << std::setw(12) << std::right << std::fixed << std::setprecision(2) << r.avg_time_ms << " ms"
              << std::setw(12) << std::fixed << std::setprecision(1) << r.throughput_fps << " fps"
              << std::setw(15) << r.total_allocations << " allocs"
              << std::setw(12) << (r.peak_memory_bytes / 1024 / 1024) << " MB"
              << std::endl;
}

// Бенчмарк: обработка с Memory Arena
BenchmarkResult benchmark_arena_processing(int iterations) {
    MemoryArena arena(32 * 1024 * 1024);  // 32 MB
    ImageProcessor processor(arena);
    
    // Создаем тестовые данные
    std::vector<uint8_t> input(FRAME_SIZE);
    std::random_device rd;
    std::mt19937 gen(rd());
    std::uniform_int_distribution<> dis(0, 255);
    for (auto& byte : input) {
        byte = dis(gen);
    }
    
    auto start = high_resolution_clock::now();
    
    for (int i = 0; i < iterations; ++i) {
        uint8_t* output = processor.gaussian_blur(input.data(), WIDTH, HEIGHT, 5.0f);
        
        // Сбрасываем арену каждые 100 кадров
        if ((i + 1) % 100 == 0) {
            arena.reset();
        }
    }
    
    auto end = high_resolution_clock::now();
    double total_ms = duration_cast<microseconds>(end - start).count() / 1000.0;
    
    return {
        "Arena + Gaussian Blur",
        total_ms,
        iterations,
        total_ms / iterations,
        iterations / (total_ms / 1000.0),
        arena.total_allocations(),
        arena.peak_usage()
    };
}

// Бенчмарк: обработка с стандартным malloc/free
BenchmarkResult benchmark_malloc_processing(int iterations) {
    std::vector<uint8_t> input(FRAME_SIZE);
    std::random_device rd;
    std::mt19937 gen(rd());
    std::uniform_int_distribution<> dis(0, 255);
    for (auto& byte : input) {
        byte = dis(gen);
    }
    
    size_t total_allocs = 0;
    size_t peak_memory = 0;
    
    auto start = high_resolution_clock::now();
    
    for (int i = 0; i < iterations; ++i) {
        // Симулируем выделение памяти как в обычном коде
        uint8_t* output = new uint8_t[FRAME_SIZE];
        float* temp = new float[FRAME_SIZE];
        float* kernel = new float[31];  // Максимальный размер ядра
        
        total_allocs += 3;
        peak_memory = std::max(peak_memory, FRAME_SIZE + FRAME_SIZE * sizeof(float) + 31 * sizeof(float));
        
        // Простая обработка для честного сравнения
        std::memcpy(output, input.data(), FRAME_SIZE);
        
        delete[] kernel;
        delete[] temp;
        delete[] output;
    }
    
    auto end = high_resolution_clock::now();
    double total_ms = duration_cast<microseconds>(end - start).count() / 1000.0;
    
    return {
        "Malloc + Simple Copy",
        total_ms,
        iterations,
        total_ms / iterations,
        iterations / (total_ms / 1000.0),
        total_allocs,
        peak_memory
    };
}

// Бенчмарк: только аллокации в арене
BenchmarkResult benchmark_arena_allocations(int iterations) {
    MemoryArena arena(64 * 1024 * 1024);
    
    auto start = high_resolution_clock::now();
    
    for (int i = 0; i < iterations; ++i) {
        // Выделяем память как для обработки кадра
        arena.allocate(FRAME_SIZE);           // output
        arena.allocate(FRAME_SIZE * 4);       // temp (float)
        arena.allocate(31 * 4);               // kernel (float)
        
        if ((i + 1) % 100 == 0) {
            arena.reset();
        }
    }
    
    auto end = high_resolution_clock::now();
    double total_ms = duration_cast<microseconds>(end - start).count() / 1000.0;
    
    return {
        "Arena Allocations Only",
        total_ms,
        iterations,
        total_ms / iterations,
        iterations / (total_ms / 1000.0),
        arena.total_allocations(),
        arena.peak_usage()
    };
}

// Бенчмарк: только malloc/free
BenchmarkResult benchmark_malloc_allocations(int iterations) {
    size_t total_allocs = 0;
    
    auto start = high_resolution_clock::now();
    
    for (int i = 0; i < iterations; ++i) {
        void* p1 = malloc(FRAME_SIZE);
        void* p2 = malloc(FRAME_SIZE * 4);
        void* p3 = malloc(31 * 4);
        
        total_allocs += 3;
        
        free(p3);
        free(p2);
        free(p1);
    }
    
    auto end = high_resolution_clock::now();
    double total_ms = duration_cast<microseconds>(end - start).count() / 1000.0;
    
    return {
        "Malloc/Free Only",
        total_ms,
        iterations,
        total_ms / iterations,
        iterations / (total_ms / 1000.0),
        total_allocs,
        0
    };
}

// Бенчмарк: множество мелких аллокаций
BenchmarkResult benchmark_small_allocations_arena(int iterations) {
    MemoryArena arena(16 * 1024 * 1024);
    
    auto start = high_resolution_clock::now();
    
    for (int i = 0; i < iterations; ++i) {
        // 100 мелких аллокаций
        for (int j = 0; j < 100; ++j) {
            arena.allocate(64 + (j % 128));
        }
        
        if ((i + 1) % 10 == 0) {
            arena.reset();
        }
    }
    
    auto end = high_resolution_clock::now();
    double total_ms = duration_cast<microseconds>(end - start).count() / 1000.0;
    
    return {
        "Arena: 100 Small Allocs/Iter",
        total_ms,
        iterations,
        total_ms / iterations,
        iterations / (total_ms / 1000.0),
        arena.total_allocations(),
        arena.peak_usage()
    };
}

BenchmarkResult benchmark_small_allocations_malloc(int iterations) {
    size_t total_allocs = 0;
    
    auto start = high_resolution_clock::now();
    
    for (int i = 0; i < iterations; ++i) {
        std::vector<void*> ptrs;
        ptrs.reserve(100);
        
        for (int j = 0; j < 100; ++j) {
            ptrs.push_back(malloc(64 + (j % 128)));
            total_allocs++;
        }
        
        for (auto* p : ptrs) {
            free(p);
        }
    }
    
    auto end = high_resolution_clock::now();
    double total_ms = duration_cast<microseconds>(end - start).count() / 1000.0;
    
    return {
        "Malloc: 100 Small Allocs/Iter",
        total_ms,
        iterations,
        total_ms / iterations,
        iterations / (total_ms / 1000.0),
        total_allocs,
        0
    };
}

int main() {
    std::cout << "=== Memory Arena Benchmark ===" << std::endl;
    std::cout << "Frame size: " << WIDTH << "x" << HEIGHT << "x" << CHANNELS 
              << " = " << (FRAME_SIZE / 1024) << " KB" << std::endl;
    std::cout << std::endl;
    
    std::cout << std::setw(35) << std::left << "Benchmark"
              << std::setw(15) << std::right << "Avg Time"
              << std::setw(12) << "Throughput"
              << std::setw(18) << "Allocations"
              << std::setw(12) << "Peak Mem"
              << std::endl;
    std::cout << std::string(90, '-') << std::endl;
    
    // Запускаем бенчмарки
    const int ITERATIONS = 1000;
    const int SMALL_ITERATIONS = 10000;
    
    // Обработка изображений
    print_result(benchmark_arena_processing(ITERATIONS));
    print_result(benchmark_malloc_processing(ITERATIONS));
    
    std::cout << std::endl;
    
    // Только аллокации
    print_result(benchmark_arena_allocations(ITERATIONS));
    print_result(benchmark_malloc_allocations(ITERATIONS));
    
    std::cout << std::endl;
    
    // Множество мелких аллокаций
    print_result(benchmark_small_allocations_arena(SMALL_ITERATIONS));
    print_result(benchmark_small_allocations_malloc(SMALL_ITERATIONS));
    
    std::cout << std::endl;
    std::cout << "=== Benchmark Complete ===" << std::endl;
    
    return 0;
}


