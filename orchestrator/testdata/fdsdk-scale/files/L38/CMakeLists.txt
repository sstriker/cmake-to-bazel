cmake_minimum_required(VERSION 3.20)
project(L38 LANGUAGES C VERSION 0.1.0)

find_package(L18 CONFIG REQUIRED)
find_package(L21 CONFIG REQUIRED)

add_library(L38 STATIC src/L38.c)
target_include_directories(L38 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L38 PUBLIC L18::L18 L21::L21)

install(TARGETS L38
        EXPORT L38Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L38Targets
        FILE L38Targets.cmake
        NAMESPACE L38::
        DESTINATION lib/cmake/L38)
