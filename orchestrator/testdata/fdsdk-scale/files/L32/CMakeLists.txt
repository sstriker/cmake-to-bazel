cmake_minimum_required(VERSION 3.20)
project(L32 LANGUAGES C VERSION 0.1.0)

find_package(L12 CONFIG REQUIRED)
find_package(L15 CONFIG REQUIRED)

add_library(L32 STATIC src/L32.c)
target_include_directories(L32 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L32 PUBLIC L12::L12 L15::L15)

install(TARGETS L32
        EXPORT L32Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L32Targets
        FILE L32Targets.cmake
        NAMESPACE L32::
        DESTINATION lib/cmake/L32)
