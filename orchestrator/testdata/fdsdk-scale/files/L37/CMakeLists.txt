cmake_minimum_required(VERSION 3.20)
project(L37 LANGUAGES C VERSION 0.1.0)

find_package(L17 CONFIG REQUIRED)
find_package(L20 CONFIG REQUIRED)

add_library(L37 STATIC src/L37.c)
target_include_directories(L37 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L37 PUBLIC L17::L17 L20::L20)

install(TARGETS L37
        EXPORT L37Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L37Targets
        FILE L37Targets.cmake
        NAMESPACE L37::
        DESTINATION lib/cmake/L37)
