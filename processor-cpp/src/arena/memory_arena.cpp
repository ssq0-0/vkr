#include "memory_arena.hpp"
#include <algorithm>
#include <stdexcept>
#include <cstring>

namespace arena {

MemoryArena::MemoryArena(size_t block_size)
    : block_size_(block_size) {
    // Выделяем первый блок сразу
    allocate_block(block_size_);
}

MemoryArena::MemoryArena(MemoryArena&& other) noexcept
    : blocks_(std::move(other.blocks_))
    , current_block_(other.current_block_)
    , block_size_(other.block_size_)
    , allocated_bytes_(other.allocated_bytes_.load())
    , used_bytes_(other.used_bytes_.load())
    , total_allocations_(other.total_allocations_.load())
    , reset_count_(other.reset_count_.load())
    , peak_usage_(other.peak_usage_.load()) {
    other.current_block_ = nullptr;
    other.allocated_bytes_ = 0;
    other.used_bytes_ = 0;
}

MemoryArena& MemoryArena::operator=(MemoryArena&& other) noexcept {
    if (this != &other) {
        blocks_ = std::move(other.blocks_);
        current_block_ = other.current_block_;
        block_size_ = other.block_size_;
        allocated_bytes_ = other.allocated_bytes_.load();
        used_bytes_ = other.used_bytes_.load();
        total_allocations_ = other.total_allocations_.load();
        reset_count_ = other.reset_count_.load();
        peak_usage_ = other.peak_usage_.load();
        
        other.current_block_ = nullptr;
        other.allocated_bytes_ = 0;
        other.used_bytes_ = 0;
    }
    return *this;
}

MemoryArena::~MemoryArena() {
    clear();
}

void* MemoryArena::allocate(size_t size, size_t alignment) {
    std::lock_guard<std::mutex> lock(mutex_);
    
    if (size == 0) return nullptr;
    
    // Проверяем, что alignment - степень 2
    if ((alignment & (alignment - 1)) != 0) {
        alignment = DEFAULT_ALIGNMENT;
    }
    
    // Пытаемся выделить из текущего блока
    if (current_block_) {
        size_t current_ptr = reinterpret_cast<size_t>(
            current_block_->data.get() + current_block_->used);
        size_t aligned_ptr = align_up(current_ptr, alignment);
        size_t padding = aligned_ptr - current_ptr;
        size_t total_size = padding + size;
        
        if (current_block_->used + total_size <= current_block_->size) {
            current_block_->used += total_size;
            used_bytes_ += total_size;
            total_allocations_++;
            
            // Обновляем пиковое использование
            size_t current_used = used_bytes_.load();
            size_t peak = peak_usage_.load();
            while (current_used > peak && 
                   !peak_usage_.compare_exchange_weak(peak, current_used)) {}
            
            return reinterpret_cast<void*>(aligned_ptr);
        }
    }
    
    // Нужен новый блок
    size_t required_size = std::max(size + alignment, block_size_);
    Block* new_block = allocate_block(required_size);
    if (!new_block) return nullptr;
    
    size_t new_ptr = reinterpret_cast<size_t>(new_block->data.get());
    size_t aligned_ptr = align_up(new_ptr, alignment);
    size_t padding = aligned_ptr - new_ptr;
    
    new_block->used = padding + size;
    used_bytes_ += new_block->used;
    total_allocations_++;
    
    // Обновляем пиковое использование
    size_t current_used = used_bytes_.load();
    size_t peak = peak_usage_.load();
    while (current_used > peak && 
           !peak_usage_.compare_exchange_weak(peak, current_used)) {}
    
    return reinterpret_cast<void*>(aligned_ptr);
}

void MemoryArena::reset() {
    std::lock_guard<std::mutex> lock(mutex_);
    
    // Сбрасываем использование во всех блоках
    for (auto& block : blocks_) {
        block->used = 0;
    }
    
    // Устанавливаем текущий блок на первый
    if (!blocks_.empty()) {
        current_block_ = blocks_.front().get();
    }
    
    used_bytes_ = 0;
    reset_count_++;
}

void MemoryArena::clear() {
    std::lock_guard<std::mutex> lock(mutex_);
    
    blocks_.clear();
    current_block_ = nullptr;
    allocated_bytes_ = 0;
    used_bytes_ = 0;
}

MemoryArena::Block* MemoryArena::allocate_block(size_t min_size) {
    size_t block_size = std::max(min_size, block_size_);
    
    try {
        auto block = std::make_unique<Block>(block_size);
        Block* ptr = block.get();
        blocks_.push_back(std::move(block));
        current_block_ = ptr;
        allocated_bytes_ += block_size;
        return ptr;
    } catch (const std::bad_alloc&) {
        return nullptr;
    }
}

// ArenaMarker implementation

ArenaMarker::ArenaMarker(MemoryArena& arena)
    : arena_(arena)
    , saved_used_bytes_(arena.used_bytes()) {}

ArenaMarker::~ArenaMarker() {
    if (!released_) {
        release();
    }
}

void ArenaMarker::release() {
    if (released_) return;
    
    // Примечание: полное восстановление состояния арены требует 
    // более сложной реализации с сохранением позиции в блоке.
    // Для простоты здесь просто помечаем как освобожденное.
    released_ = true;
}

// ThreadLocalArena implementation

thread_local MemoryArena ThreadLocalArena::arena_(MemoryArena::DEFAULT_BLOCK_SIZE);

MemoryArena& ThreadLocalArena::get() {
    return arena_;
}

void ThreadLocalArena::reset_all() {
    arena_.reset();
}

} // namespace arena


