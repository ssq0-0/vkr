#include "image_processor.hpp"
#include <cmath>
#include <algorithm>
#include <cstring>

#if defined(__x86_64__) || defined(_M_X64)
#include <immintrin.h>
#define HAS_SIMD 1
#elif defined(__aarch64__)
#include <arm_neon.h>
#define HAS_NEON 1
#endif

namespace processing {

ImageProcessor::ImageProcessor(arena::MemoryArena& arena)
    : arena_(arena) {}

void ImageProcessor::generate_gaussian_kernel(float* kernel, int size, float sigma) {
    float sum = 0.0f;
    int half = size / 2;
    float sigma2 = 2.0f * sigma * sigma;
    
    for (int i = 0; i < size; ++i) {
        float x = static_cast<float>(i - half);
        kernel[i] = std::exp(-(x * x) / sigma2);
        sum += kernel[i];
    }
    
    // Нормализация
    for (int i = 0; i < size; ++i) {
        kernel[i] /= sum;
    }
}

void ImageProcessor::gaussian_blur(const Image& input, Image& output, float radius) {
    // Вычисляем размер ядра (6 * sigma покрывает 99.7% распределения)
    int kernel_size = static_cast<int>(std::ceil(radius * 6)) | 1;  // Делаем нечетным
    kernel_size = std::max(3, std::min(kernel_size, 31));  // Ограничиваем размер
    
    // Выделяем ядро из арены
    float* kernel = arena_.allocate_array<float>(kernel_size);
    generate_gaussian_kernel(kernel, kernel_size, radius);
    
    // Применяем сепарабельную свертку
    convolve_separable(input.data, output.data, 
                       input.width, input.height, input.channels,
                       kernel, kernel_size);
}

uint8_t* ImageProcessor::gaussian_blur(const uint8_t* input, int width, int height, float radius) {
    size_t size = width * height * 3;
    
    // Выделяем память для результата из арены
    uint8_t* output = arena_.allocate_array<uint8_t>(size);
    
    Image img_in = {const_cast<uint8_t*>(input), width, height, 3};
    Image img_out = {output, width, height, 3};
    
    gaussian_blur(img_in, img_out, radius);
    
    return output;
}

void ImageProcessor::convolve_separable(const uint8_t* input, uint8_t* output,
                                        int width, int height, int channels,
                                        const float* kernel, int kernel_size) {
    size_t temp_size = width * height * channels * sizeof(float);
    float* temp = arena_.allocate_array<float>(width * height * channels);
    
    // Горизонтальный проход
    convolve_horizontal_simd(input, temp, width, height, channels, kernel, kernel_size);
    
    // Вертикальный проход
    convolve_vertical_simd(temp, output, width, height, channels, kernel, kernel_size);
}

void ImageProcessor::convolve_horizontal_simd(const uint8_t* input, float* temp,
                                              int width, int height, int channels,
                                              const float* kernel, int kernel_size) {
    int half = kernel_size / 2;
    
#if HAS_SIMD && defined(__AVX2__)
    // AVX2 оптимизированная версия
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            __m256 sum_r = _mm256_setzero_ps();
            __m256 sum_g = _mm256_setzero_ps();
            __m256 sum_b = _mm256_setzero_ps();
            
            int k = 0;
            for (; k + 7 < kernel_size; k += 8) {
                int kx = x - half + k;
                __m256 kvals = _mm256_loadu_ps(&kernel[k]);
                
                // Загружаем 8 пикселей (с граничными проверками)
                float r[8], g[8], b[8];
                for (int i = 0; i < 8; ++i) {
                    int px = std::max(0, std::min(kx + i, width - 1));
                    int idx = (y * width + px) * channels;
                    r[i] = input[idx];
                    g[i] = input[idx + 1];
                    b[i] = input[idx + 2];
                }
                
                __m256 vr = _mm256_loadu_ps(r);
                __m256 vg = _mm256_loadu_ps(g);
                __m256 vb = _mm256_loadu_ps(b);
                
                sum_r = _mm256_fmadd_ps(vr, kvals, sum_r);
                sum_g = _mm256_fmadd_ps(vg, kvals, sum_g);
                sum_b = _mm256_fmadd_ps(vb, kvals, sum_b);
            }
            
            // Суммируем компоненты вектора
            float result_r[8], result_g[8], result_b[8];
            _mm256_storeu_ps(result_r, sum_r);
            _mm256_storeu_ps(result_g, sum_g);
            _mm256_storeu_ps(result_b, sum_b);
            
            float final_r = 0, final_g = 0, final_b = 0;
            for (int i = 0; i < 8; ++i) {
                final_r += result_r[i];
                final_g += result_g[i];
                final_b += result_b[i];
            }
            
            // Обрабатываем оставшиеся элементы
            for (; k < kernel_size; ++k) {
                int kx = std::max(0, std::min(x - half + k, width - 1));
                int idx = (y * width + kx) * channels;
                final_r += input[idx] * kernel[k];
                final_g += input[idx + 1] * kernel[k];
                final_b += input[idx + 2] * kernel[k];
            }
            
            int out_idx = (y * width + x) * channels;
            temp[out_idx] = final_r;
            temp[out_idx + 1] = final_g;
            temp[out_idx + 2] = final_b;
        }
    }
#else
    // Скалярная версия
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            float sum_r = 0, sum_g = 0, sum_b = 0;
            
            for (int k = 0; k < kernel_size; ++k) {
                int kx = std::max(0, std::min(x - half + k, width - 1));
                int idx = (y * width + kx) * channels;
                sum_r += input[idx] * kernel[k];
                sum_g += input[idx + 1] * kernel[k];
                sum_b += input[idx + 2] * kernel[k];
            }
            
            int out_idx = (y * width + x) * channels;
            temp[out_idx] = sum_r;
            temp[out_idx + 1] = sum_g;
            temp[out_idx + 2] = sum_b;
        }
    }
#endif
}

void ImageProcessor::convolve_vertical_simd(const float* temp, uint8_t* output,
                                            int width, int height, int channels,
                                            const float* kernel, int kernel_size) {
    int half = kernel_size / 2;
    
#if HAS_SIMD && defined(__AVX2__)
    // AVX2 оптимизированная версия
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width - 7; x += 8) {
            __m256 sum_r = _mm256_setzero_ps();
            __m256 sum_g = _mm256_setzero_ps();
            __m256 sum_b = _mm256_setzero_ps();
            
            for (int k = 0; k < kernel_size; ++k) {
                int ky = std::max(0, std::min(y - half + k, height - 1));
                __m256 kval = _mm256_set1_ps(kernel[k]);
                
                float r[8], g[8], b[8];
                for (int i = 0; i < 8; ++i) {
                    int idx = (ky * width + x + i) * channels;
                    r[i] = temp[idx];
                    g[i] = temp[idx + 1];
                    b[i] = temp[idx + 2];
                }
                
                __m256 vr = _mm256_loadu_ps(r);
                __m256 vg = _mm256_loadu_ps(g);
                __m256 vb = _mm256_loadu_ps(b);
                
                sum_r = _mm256_fmadd_ps(vr, kval, sum_r);
                sum_g = _mm256_fmadd_ps(vg, kval, sum_g);
                sum_b = _mm256_fmadd_ps(vb, kval, sum_b);
            }
            
            // Сохраняем результат с clamp к [0, 255]
            float result_r[8], result_g[8], result_b[8];
            _mm256_storeu_ps(result_r, sum_r);
            _mm256_storeu_ps(result_g, sum_g);
            _mm256_storeu_ps(result_b, sum_b);
            
            for (int i = 0; i < 8; ++i) {
                int out_idx = (y * width + x + i) * channels;
                output[out_idx] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, result_r[i])));
                output[out_idx + 1] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, result_g[i])));
                output[out_idx + 2] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, result_b[i])));
            }
        }
        
        // Обрабатываем оставшиеся пиксели
        for (int x = (width / 8) * 8; x < width; ++x) {
            float sum_r = 0, sum_g = 0, sum_b = 0;
            
            for (int k = 0; k < kernel_size; ++k) {
                int ky = std::max(0, std::min(y - half + k, height - 1));
                int idx = (ky * width + x) * channels;
                sum_r += temp[idx] * kernel[k];
                sum_g += temp[idx + 1] * kernel[k];
                sum_b += temp[idx + 2] * kernel[k];
            }
            
            int out_idx = (y * width + x) * channels;
            output[out_idx] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum_r)));
            output[out_idx + 1] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum_g)));
            output[out_idx + 2] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum_b)));
        }
    }
#else
    // Скалярная версия
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            float sum_r = 0, sum_g = 0, sum_b = 0;
            
            for (int k = 0; k < kernel_size; ++k) {
                int ky = std::max(0, std::min(y - half + k, height - 1));
                int idx = (ky * width + x) * channels;
                sum_r += temp[idx] * kernel[k];
                sum_g += temp[idx + 1] * kernel[k];
                sum_b += temp[idx + 2] * kernel[k];
            }
            
            int out_idx = (y * width + x) * channels;
            output[out_idx] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum_r)));
            output[out_idx + 1] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum_g)));
            output[out_idx + 2] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum_b)));
        }
    }
#endif
}

void ImageProcessor::sharpen(const Image& input, Image& output, float intensity) {
    // Ядро повышения резкости
    float kernel[9] = {
        0, -intensity, 0,
        -intensity, 1 + 4 * intensity, -intensity,
        0, -intensity, 0
    };
    
    int width = input.width;
    int height = input.height;
    int channels = input.channels;
    
    for (int y = 1; y < height - 1; ++y) {
        for (int x = 1; x < width - 1; ++x) {
            for (int c = 0; c < channels; ++c) {
                float sum = 0;
                for (int ky = -1; ky <= 1; ++ky) {
                    for (int kx = -1; kx <= 1; ++kx) {
                        int idx = ((y + ky) * width + (x + kx)) * channels + c;
                        sum += input.data[idx] * kernel[(ky + 1) * 3 + (kx + 1)];
                    }
                }
                int out_idx = (y * width + x) * channels + c;
                output.data[out_idx] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, sum)));
            }
        }
    }
}

void ImageProcessor::edge_detect(const Image& input, Image& output) {
    // Sobel operators
    int sobel_x[9] = {-1, 0, 1, -2, 0, 2, -1, 0, 1};
    int sobel_y[9] = {-1, -2, -1, 0, 0, 0, 1, 2, 1};
    
    int width = input.width;
    int height = input.height;
    
    // Сначала конвертируем в градации серого
    uint8_t* gray = arena_.allocate_array<uint8_t>(width * height);
    for (int i = 0; i < width * height; ++i) {
        int idx = i * 3;
        gray[i] = static_cast<uint8_t>(
            0.299f * input.data[idx] + 
            0.587f * input.data[idx + 1] + 
            0.114f * input.data[idx + 2]);
    }
    
    // Применяем Sobel
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
            int magnitude = static_cast<int>(std::sqrt(gx * gx + gy * gy));
            magnitude = std::min(255, magnitude);
            
            int out_idx = (y * width + x) * 3;
            output.data[out_idx] = magnitude;
            output.data[out_idx + 1] = magnitude;
            output.data[out_idx + 2] = magnitude;
        }
    }
}

void ImageProcessor::grayscale(const Image& input, Image& output) {
    int size = input.width * input.height;
    
    for (int i = 0; i < size; ++i) {
        int idx = i * 3;
        uint8_t gray = static_cast<uint8_t>(
            0.299f * input.data[idx] + 
            0.587f * input.data[idx + 1] + 
            0.114f * input.data[idx + 2]);
        output.data[idx] = gray;
        output.data[idx + 1] = gray;
        output.data[idx + 2] = gray;
    }
}

// SIMD utility functions
namespace simd {

bool has_sse() {
#if defined(__x86_64__) || defined(_M_X64)
    return true;  // x86_64 всегда имеет SSE
#else
    return false;
#endif
}

bool has_avx() {
#if defined(__AVX__)
    return true;
#else
    return false;
#endif
}

bool has_avx2() {
#if defined(__AVX2__)
    return true;
#else
    return false;
#endif
}

void multiply_add_f32(float* dst, const float* src, float multiplier, size_t count) {
#if HAS_SIMD && defined(__AVX__)
    __m256 mult = _mm256_set1_ps(multiplier);
    size_t i = 0;
    for (; i + 7 < count; i += 8) {
        __m256 s = _mm256_loadu_ps(&src[i]);
        __m256 d = _mm256_loadu_ps(&dst[i]);
        d = _mm256_fmadd_ps(s, mult, d);
        _mm256_storeu_ps(&dst[i], d);
    }
    for (; i < count; ++i) {
        dst[i] += src[i] * multiplier;
    }
#else
    for (size_t i = 0; i < count; ++i) {
        dst[i] += src[i] * multiplier;
    }
#endif
}

void clamp_f32_to_u8(uint8_t* dst, const float* src, size_t count) {
    for (size_t i = 0; i < count; ++i) {
        dst[i] = static_cast<uint8_t>(std::max(0.0f, std::min(255.0f, src[i])));
    }
}

} // namespace simd

} // namespace processing


