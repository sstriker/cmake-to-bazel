cmake_minimum_required(VERSION 3.20)
project(L34 LANGUAGES C VERSION 0.1.0)

find_package(L14 CONFIG REQUIRED)
find_package(L17 CONFIG REQUIRED)

add_library(L34 STATIC src/L34.c)
target_include_directories(L34 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L34 PUBLIC L14::L14 L17::L17)

install(TARGETS L34
        EXPORT L34Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L34Targets
        FILE L34Targets.cmake
        NAMESPACE L34::
        DESTINATION lib/cmake/L34)
