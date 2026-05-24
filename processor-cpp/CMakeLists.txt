cmake_minimum_required(VERSION 3.16)
project(streaming_processor_cpp VERSION 1.0.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

# Оптимизации компиляции
if(NOT CMAKE_BUILD_TYPE)
    set(CMAKE_BUILD_TYPE Release)
endif()

set(CMAKE_CXX_FLAGS_RELEASE "-O3 -DNDEBUG")
set(CMAKE_CXX_FLAGS_DEBUG "-g -O0")

# SIMD флаги (отключаем для ARM Mac)
if(CMAKE_SYSTEM_PROCESSOR MATCHES "x86_64")
    include(CheckCXXCompilerFlag)
    check_cxx_compiler_flag("-mavx2" COMPILER_SUPPORTS_AVX2)
    check_cxx_compiler_flag("-mavx" COMPILER_SUPPORTS_AVX)
    check_cxx_compiler_flag("-msse4.1" COMPILER_SUPPORTS_SSE41)

    if(COMPILER_SUPPORTS_AVX2)
        set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -mavx2 -mfma")
    elseif(COMPILER_SUPPORTS_AVX)
        set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -mavx")
    elseif(COMPILER_SUPPORTS_SSE41)
        set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -msse4.1")
    endif()
endif()

# Поиск зависимостей через pkg-config
find_package(Threads REQUIRED)
find_package(PkgConfig REQUIRED)

pkg_check_modules(PROTOBUF REQUIRED protobuf)
pkg_check_modules(GRPC REQUIRED grpc++)

# Добавляем пути к библиотекам
link_directories(${PROTOBUF_LIBRARY_DIRS} ${GRPC_LIBRARY_DIRS})

# Находим программы protoc и grpc_cpp_plugin
find_program(_PROTOBUF_PROTOC protoc REQUIRED)
find_program(_GRPC_CPP_PLUGIN grpc_cpp_plugin REQUIRED)

# Генерация protobuf файлов
set(PROTO_FILE ${CMAKE_SOURCE_DIR}/../proto/streaming.proto)
set(PROTO_BINARY_DIR ${CMAKE_CURRENT_BINARY_DIR}/generated)
file(MAKE_DIRECTORY ${PROTO_BINARY_DIR})

# Генерируем .pb.cc и .grpc.pb.cc
add_custom_command(
    OUTPUT "${PROTO_BINARY_DIR}/streaming.pb.cc"
           "${PROTO_BINARY_DIR}/streaming.pb.h"
           "${PROTO_BINARY_DIR}/streaming.grpc.pb.cc"
           "${PROTO_BINARY_DIR}/streaming.grpc.pb.h"
    COMMAND ${_PROTOBUF_PROTOC}
    ARGS --grpc_out "${PROTO_BINARY_DIR}"
         --cpp_out "${PROTO_BINARY_DIR}"
         -I "${CMAKE_SOURCE_DIR}/../proto"
         --plugin=protoc-gen-grpc="${_GRPC_CPP_PLUGIN}"
         "${PROTO_FILE}"
    DEPENDS "${PROTO_FILE}"
    COMMENT "Running protoc and grpc_cpp_plugin on streaming.proto"
)

# Библиотека с сгенерированными protobuf файлами
add_library(streaming_proto
    ${PROTO_BINARY_DIR}/streaming.pb.cc
    ${PROTO_BINARY_DIR}/streaming.grpc.pb.cc
)
target_include_directories(streaming_proto PUBLIC 
    ${PROTO_BINARY_DIR}
    ${PROTOBUF_INCLUDE_DIRS}
    ${GRPC_INCLUDE_DIRS}
)
target_link_libraries(streaming_proto PUBLIC
    ${PROTOBUF_LIBRARIES}
    ${GRPC_LIBRARIES}
)
target_compile_options(streaming_proto PUBLIC ${PROTOBUF_CFLAGS_OTHER} ${GRPC_CFLAGS_OTHER})

# Основная библиотека
add_library(processor_lib
    src/arena/memory_arena.cpp
    src/processing/image_processor.cpp
    src/server/grpc_server.cpp
)
target_include_directories(processor_lib PUBLIC 
    ${CMAKE_SOURCE_DIR}/src
    ${PROTO_BINARY_DIR}
    ${PROTOBUF_INCLUDE_DIRS}
    ${GRPC_INCLUDE_DIRS}
)
target_link_libraries(processor_lib PUBLIC
    streaming_proto
    Threads::Threads
)

# Основной исполняемый файл
add_executable(processor_server
    src/main.cpp
    src/server/metrics_server.cpp
)
target_link_libraries(processor_server PRIVATE processor_lib)

# Бенчмарк для Memory Arena (без grpc зависимостей)
add_executable(arena_benchmark
    tests/arena_benchmark.cpp
    src/arena/memory_arena.cpp
    src/processing/image_processor.cpp
)
target_include_directories(arena_benchmark PRIVATE ${CMAKE_SOURCE_DIR}/src)
target_link_libraries(arena_benchmark PRIVATE Threads::Threads)

# Тесты
enable_testing()

add_executable(test_arena tests/test_arena.cpp src/arena/memory_arena.cpp)
target_include_directories(test_arena PRIVATE ${CMAKE_SOURCE_DIR}/src)
target_link_libraries(test_arena PRIVATE Threads::Threads)
add_test(NAME test_arena COMMAND test_arena)

add_executable(test_processor 
    tests/test_processor.cpp 
    src/arena/memory_arena.cpp 
    src/processing/image_processor.cpp
)
target_include_directories(test_processor PRIVATE ${CMAKE_SOURCE_DIR}/src)
target_link_libraries(test_processor PRIVATE Threads::Threads)
add_test(NAME test_processor COMMAND test_processor)

# Установка
install(TARGETS processor_server arena_benchmark
    RUNTIME DESTINATION bin
)

# Вывод информации о конфигурации
message(STATUS "Build type: ${CMAKE_BUILD_TYPE}")
message(STATUS "CXX flags: ${CMAKE_CXX_FLAGS}")
message(STATUS "System processor: ${CMAKE_SYSTEM_PROCESSOR}")
