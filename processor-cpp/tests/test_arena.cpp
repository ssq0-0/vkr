#include <iostream>
#include <cassert>
#include <cstring>
#include <vector>
#include "../src/arena/memory_arena.hpp"

using namespace arena;

void test_basic_allocation() {
    std::cout << "Test: Basic allocation... ";
    
    MemoryArena arena(1024);
    
    void* ptr1 = arena.allocate(100);
    assert(ptr1 != nullptr);
    assert(arena.used_bytes() >= 100);
    
    void* ptr2 = arena.allocate(200);
    assert(ptr2 != nullptr);
    assert(ptr2 != ptr1);
    
    std::cout << "PASSED" << std::endl;
}

void test_alignment() {
    std::cout << "Test: Alignment... ";
    
    MemoryArena arena(4096);
    
    // Выделяем с разным выравниванием
    void* ptr1 = arena.allocate(1, 1);
    void* ptr16 = arena.allocate(1, 16);
    void* ptr64 = arena.allocate(1, 64);
    
    assert(reinterpret_cast<uintptr_t>(ptr16) % 16 == 0);
    assert(reinterpret_cast<uintptr_t>(ptr64) % 64 == 0);
    
    std::cout << "PASSED" << std::endl;
}

void test_reset() {
    std::cout << "Test: Reset... ";
    
    MemoryArena arena(4096);
    
    // Выделяем память
    for (int i = 0; i < 10; ++i) {
        arena.allocate(100);
    }
    
    size_t used_before = arena.used_bytes();
    assert(used_before > 0);
    
    // Сбрасываем
    arena.reset();
    assert(arena.used_bytes() == 0);
    assert(arena.reset_count() == 1);
    
    // Проверяем, что можем снова выделять
    void* ptr = arena.allocate(100);
    assert(ptr != nullptr);
    
    std::cout << "PASSED" << std::endl;
}

void test_large_allocation() {
    std::cout << "Test: Large allocation... ";
    
    MemoryArena arena(1024);  // Маленький начальный размер
    
    // Выделяем больше, чем размер блока
    void* ptr = arena.allocate(2048);
    assert(ptr != nullptr);
    
    // Должен был выделить новый блок
    assert(arena.block_count() >= 2);
    
    std::cout << "PASSED" << std::endl;
}

void test_create_objects() {
    std::cout << "Test: Create objects... ";
    
    struct TestStruct {
        int a;
        double b;
        char c[32];
        
        TestStruct(int a_, double b_) : a(a_), b(b_) {
            std::memset(c, 0, sizeof(c));
        }
    };
    
    MemoryArena arena(4096);
    
    TestStruct* obj = arena.create<TestStruct>(42, 3.14);
    assert(obj != nullptr);
    assert(obj->a == 42);
    assert(obj->b == 3.14);
    
    std::cout << "PASSED" << std::endl;
}

void test_allocate_array() {
    std::cout << "Test: Allocate array... ";
    
    MemoryArena arena(4096);
    
    int* arr = arena.allocate_array<int>(100);
    assert(arr != nullptr);
    
    // Записываем и проверяем данные
    for (int i = 0; i < 100; ++i) {
        arr[i] = i * 2;
    }
    
    for (int i = 0; i < 100; ++i) {
        assert(arr[i] == i * 2);
    }
    
    std::cout << "PASSED" << std::endl;
}

void test_statistics() {
    std::cout << "Test: Statistics... ";
    
    MemoryArena arena(4096);
    
    assert(arena.total_allocations() == 0);
    
    arena.allocate(100);
    assert(arena.total_allocations() == 1);
    
    arena.allocate(200);
    assert(arena.total_allocations() == 2);
    
    arena.reset();
    // После reset количество аллокаций сохраняется
    
    std::cout << "PASSED" << std::endl;
}

void test_thread_local_arena() {
    std::cout << "Test: Thread local arena... ";
    
    auto& arena = ThreadLocalArena::get();
    
    void* ptr = arena.allocate(100);
    assert(ptr != nullptr);
    
    ThreadLocalArena::reset_all();
    assert(arena.used_bytes() == 0);
    
    std::cout << "PASSED" << std::endl;
}

int main() {
    std::cout << "=== Memory Arena Unit Tests ===" << std::endl;
    
    test_basic_allocation();
    test_alignment();
    test_reset();
    test_large_allocation();
    test_create_objects();
    test_allocate_array();
    test_statistics();
    test_thread_local_arena();
    
    std::cout << "\nAll tests passed!" << std::endl;
    return 0;
}


