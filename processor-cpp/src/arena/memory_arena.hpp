#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>
#include <memory>
#include <atomic>
#include <mutex>

namespace arena {

/**
 * Memory Arena - эффективный аллокатор памяти для потоковой обработки.
 * 
 * Основные преимущества:
 * - Минимизация системных вызовов malloc/free
 * - Быстрое освобождение всех объектов разом (reset)
 * - Улучшенная cache locality
 * - Предсказуемое использование памяти
 */
class MemoryArena {
public:
    // Размер блока по умолчанию (4 MB)
    static constexpr size_t DEFAULT_BLOCK_SIZE = 4 * 1024 * 1024;
    
    // Выравнивание по умолчанию
    static constexpr size_t DEFAULT_ALIGNMENT = 16;

    /**
     * Конструктор
     * @param block_size Размер одного блока памяти
     */
    explicit MemoryArena(size_t block_size = DEFAULT_BLOCK_SIZE);
    
    // Запрещаем копирование
    MemoryArena(const MemoryArena&) = delete;
    MemoryArena& operator=(const MemoryArena&) = delete;
    
    // Разрешаем перемещение
    MemoryArena(MemoryArena&& other) noexcept;
    MemoryArena& operator=(MemoryArena&& other) noexcept;
    
    ~MemoryArena();

    /**
     * Выделить память из арены
     * @param size Размер в байтах
     * @param alignment Выравнивание (должно быть степенью 2)
     * @return Указатель на выделенную память или nullptr при ошибке
     */
    void* allocate(size_t size, size_t alignment = DEFAULT_ALIGNMENT);
    
    /**
     * Выделить и инициализировать объект типа T
     * @tparam T Тип объекта
     * @tparam Args Типы аргументов конструктора
     * @param args Аргументы для конструктора
     * @return Указатель на созданный объект
     */
    template<typename T, typename... Args>
    T* create(Args&&... args) {
        void* ptr = allocate(sizeof(T), alignof(T));
        if (!ptr) return nullptr;
        return new(ptr) T(std::forward<Args>(args)...);
    }
    
    /**
     * Выделить массив элементов типа T
     * @tparam T Тип элементов
     * @param count Количество элементов
     * @return Указатель на начало массива
     */
    template<typename T>
    T* allocate_array(size_t count) {
        void* ptr = allocate(sizeof(T) * count, alignof(T));
        return static_cast<T*>(ptr);
    }

    /**
     * Сбросить арену - освободить всю выделенную память для переиспользования
     * Не освобождает блоки, а лишь сбрасывает указатели
     */
    void reset();
    
    /**
     * Полностью освободить всю память
     */
    void clear();

    // Статистика
    size_t allocated_bytes() const { return allocated_bytes_.load(); }
    size_t used_bytes() const { return used_bytes_.load(); }
    size_t total_allocations() const { return total_allocations_.load(); }
    size_t block_count() const { return blocks_.size(); }
    size_t reset_count() const { return reset_count_.load(); }
    size_t peak_usage() const { return peak_usage_.load(); }

private:
    struct Block {
        std::unique_ptr<uint8_t[]> data;
        size_t size;
        size_t used;
        
        Block(size_t sz) : data(new uint8_t[sz]), size(sz), used(0) {}
    };
    
    // Выделить новый блок памяти
    Block* allocate_block(size_t min_size);
    
    // Выровнять указатель
    static size_t align_up(size_t value, size_t alignment) {
        return (value + alignment - 1) & ~(alignment - 1);
    }

    std::vector<std::unique_ptr<Block>> blocks_;
    Block* current_block_ = nullptr;
    size_t block_size_;
    
    // Статистика (атомарные для thread-safety)
    std::atomic<size_t> allocated_bytes_{0};
    std::atomic<size_t> used_bytes_{0};
    std::atomic<size_t> total_allocations_{0};
    std::atomic<size_t> reset_count_{0};
    std::atomic<size_t> peak_usage_{0};
    
    mutable std::mutex mutex_;
};

/**
 * Scoped Arena Marker - сохраняет состояние арены и восстанавливает при выходе из scope
 */
class ArenaMarker {
public:
    explicit ArenaMarker(MemoryArena& arena);
    ~ArenaMarker();
    
    // Запрещаем копирование и перемещение
    ArenaMarker(const ArenaMarker&) = delete;
    ArenaMarker& operator=(const ArenaMarker&) = delete;
    
    void release();  // Освободить до маркера вручную

private:
    MemoryArena& arena_;
    size_t saved_used_bytes_;
    bool released_ = false;
};

/**
 * Thread-local Arena для многопоточной обработки
 */
class ThreadLocalArena {
public:
    static MemoryArena& get();
    static void reset_all();
    
private:
    static thread_local MemoryArena arena_;
};

} // namespace arena


