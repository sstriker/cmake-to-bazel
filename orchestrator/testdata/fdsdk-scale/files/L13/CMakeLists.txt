cmake_minimum_required(VERSION 3.20)
project(L13 LANGUAGES C VERSION 0.1.0)

find_package(L03 CONFIG REQUIRED)
find_package(L04 CONFIG REQUIRED)

add_library(L13 STATIC src/L13.c)
target_include_directories(L13 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L13 PUBLIC L03::L03 L04::L04)

install(TARGETS L13
        EXPORT L13Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L13Targets
        FILE L13Targets.cmake
        NAMESPACE L13::
        DESTINATION lib/cmake/L13)
