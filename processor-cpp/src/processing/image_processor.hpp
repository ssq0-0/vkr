#pragma once

#include <cstdint>
#include <cstddef>
#include <vector>
#include "../arena/memory_arena.hpp"

namespace processing {

/**
 * Структура для представления изображения
 */
struct Image {
    uint8_t* data;
    int width;
    int height;
    int channels;  // 3 для RGB
    
    size_t size() const { return width * height * channels; }
};

/**
 * Класс для обработки изображений с использованием Memory Arena
 */
class ImageProcessor {
public:
    explicit ImageProcessor(arena::MemoryArena& arena);
    
    /**
     * Применить Gaussian blur к изображению
     * @param input Входное изображение
     * @param output Выходное изображение (должно быть выделено заранее)
     * @param radius Радиус размытия
     */
    void gaussian_blur(const Image& input, Image& output, float radius);
    
    /**
     * Применить Gaussian blur к изображению (версия с выделением памяти из арены)
     * @param input Входные данные (RGB)
     * @param width Ширина
     * @param height Высота
     * @param radius Радиус размытия
     * @return Указатель на обработанные данные (память из арены)
     */
    uint8_t* gaussian_blur(const uint8_t* input, int width, int height, float radius);
    
    /**
     * Применить фильтр повышения резкости
     */
    void sharpen(const Image& input, Image& output, float intensity);
    
    /**
     * Применить детектор границ (Sobel)
     */
    void edge_detect(const Image& input, Image& output);
    
    /**
     * Конвертировать в градации серого
     */
    void grayscale(const Image& input, Image& output);

private:
    arena::MemoryArena& arena_;
    
    // Вспомогательные методы
    void generate_gaussian_kernel(float* kernel, int size, float sigma);
    void convolve_separable(const uint8_t* input, uint8_t* output,
                           int width, int height, int channels,
                           const float* kernel, int kernel_size);
    
    // SIMD оптимизированные версии
    void convolve_horizontal_simd(const uint8_t* input, float* temp,
                                  int width, int height, int channels,
                                  const float* kernel, int kernel_size);
    void convolve_vertical_simd(const float* temp, uint8_t* output,
                                int width, int height, int channels,
                                const float* kernel, int kernel_size);
};

/**
 * Вспомогательные функции для SIMD оптимизаций
 */
namespace simd {

// Проверка поддержки SSE/AVX
bool has_sse();
bool has_avx();
bool has_avx2();

// Векторизованные операции
void multiply_add_f32(float* dst, const float* src, float multiplier, size_t count);
void clamp_f32_to_u8(uint8_t* dst, const float* src, size_t count);

} // namespace simd

} // namespace processing


