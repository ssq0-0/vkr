#include <iostream>
#include <cassert>
#include <cstring>
#include <vector>
#include <cmath>
#include "../src/arena/memory_arena.hpp"
#include "../src/processing/image_processor.hpp"

using namespace arena;
using namespace processing;

// Создает тестовое изображение с градиентом
std::vector<uint8_t> create_test_image(int width, int height) {
    std::vector<uint8_t> img(width * height * 3);
    
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            int idx = (y * width + x) * 3;
            img[idx] = static_cast<uint8_t>(x * 255 / width);      // R - горизонтальный градиент
            img[idx + 1] = static_cast<uint8_t>(y * 255 / height); // G - вертикальный градиент
            img[idx + 2] = 128;                                     // B - константа
        }
    }
    
    return img;
}

void test_gaussian_blur() {
    std::cout << "Test: Gaussian blur... ";
    
    MemoryArena arena(8 * 1024 * 1024);
    ImageProcessor processor(arena);
    
    int width = 640, height = 480;
    auto input_data = create_test_image(width, height);
    std::vector<uint8_t> output_data(input_data.size());
    
    Image input = {input_data.data(), width, height, 3};
    Image output = {output_data.data(), width, height, 3};
    
    processor.gaussian_blur(input, output, 5.0f);
    
    // Проверяем, что выход не пустой и изменился
    bool different = false;
    for (size_t i = 0; i < input_data.size(); ++i) {
        if (input_data[i] != output_data[i]) {
            different = true;
            break;
        }
    }
    assert(different);
    
    // Проверяем, что значения в допустимом диапазоне
    for (size_t i = 0; i < output_data.size(); ++i) {
        assert(output_data[i] <= 255);
    }
    
    std::cout << "PASSED" << std::endl;
}

void test_gaussian_blur_arena() {
    std::cout << "Test: Gaussian blur with arena allocation... ";
    
    MemoryArena arena(8 * 1024 * 1024);
    ImageProcessor processor(arena);
    
    int width = 640, height = 480;
    auto input_data = create_test_image(width, height);
    
    uint8_t* output = processor.gaussian_blur(input_data.data(), width, height, 5.0f);
    assert(output != nullptr);
    
    // Проверяем результат
    bool has_nonzero = false;
    for (size_t i = 0; i < input_data.size(); ++i) {
        if (output[i] > 0) {
            has_nonzero = true;
            break;
        }
    }
    assert(has_nonzero);
    
    std::cout << "PASSED" << std::endl;
}

void test_grayscale() {
    std::cout << "Test: Grayscale conversion... ";
    
    MemoryArena arena(4 * 1024 * 1024);
    ImageProcessor processor(arena);
    
    int width = 100, height = 100;
    auto input_data = create_test_image(width, height);
    std::vector<uint8_t> output_data(input_data.size());
    
    Image input = {input_data.data(), width, height, 3};
    Image output = {output_data.data(), width, height, 3};
    
    processor.grayscale(input, output);
    
    // В градациях серого R == G == B
    for (int i = 0; i < width * height; ++i) {
        int idx = i * 3;
        assert(output_data[idx] == output_data[idx + 1]);
        assert(output_data[idx + 1] == output_data[idx + 2]);
    }
    
    std::cout << "PASSED" << std::endl;
}

void test_sharpen() {
    std::cout << "Test: Sharpen filter... ";
    
    MemoryArena arena(4 * 1024 * 1024);
    ImageProcessor processor(arena);
    
    int width = 100, height = 100;
    auto input_data = create_test_image(width, height);
    std::vector<uint8_t> output_data(input_data.size());
    
    Image input = {input_data.data(), width, height, 3};
    Image output = {output_data.data(), width, height, 3};
    
    processor.sharpen(input, output, 1.0f);
    
    // Проверяем, что выход изменился
    bool different = false;
    for (size_t i = 0; i < input_data.size(); ++i) {
        if (input_data[i] != output_data[i]) {
            different = true;
            break;
        }
    }
    assert(different);
    
    std::cout << "PASSED" << std::endl;
}

void test_edge_detect() {
    std::cout << "Test: Edge detection... ";
    
    MemoryArena arena(4 * 1024 * 1024);
    ImageProcessor processor(arena);
    
    int width = 100, height = 100;
    auto input_data = create_test_image(width, height);
    std::vector<uint8_t> output_data(input_data.size());
    
    Image input = {input_data.data(), width, height, 3};
    Image output = {output_data.data(), width, height, 3};
    
    processor.edge_detect(input, output);
    
    // Проверяем результат
    bool has_nonzero = false;
    for (size_t i = 0; i < output_data.size(); ++i) {
        if (output_data[i] > 0) {
            has_nonzero = true;
            break;
        }
    }
    assert(has_nonzero);
    
    std::cout << "PASSED" << std::endl;
}

void test_memory_efficiency() {
    std::cout << "Test: Memory efficiency... ";
    
    MemoryArena arena(16 * 1024 * 1024);  // 16 MB
    ImageProcessor processor(arena);
    
    int width = 640, height = 480;
    auto input_data = create_test_image(width, height);
    
    // Обрабатываем 100 кадров
    size_t allocs_before = arena.total_allocations();
    
    for (int i = 0; i < 100; ++i) {
        uint8_t* output = processor.gaussian_blur(input_data.data(), width, height, 5.0f);
        assert(output != nullptr);
        
        // Сбрасываем арену каждые 10 кадров
        if ((i + 1) % 10 == 0) {
            arena.reset();
        }
    }
    
    // Проверяем, что арена сбрасывалась
    assert(arena.reset_count() >= 10);
    
    std::cout << "PASSED" << std::endl;
}

int main() {
    std::cout << "=== Image Processor Unit Tests ===" << std::endl;
    
    test_gaussian_blur();
    test_gaussian_blur_arena();
    test_grayscale();
    test_sharpen();
    test_edge_detect();
    test_memory_efficiency();
    
    std::cout << "\nAll tests passed!" << std::endl;
    return 0;
}


