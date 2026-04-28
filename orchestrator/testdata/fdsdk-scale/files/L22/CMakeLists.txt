cmake_minimum_required(VERSION 3.20)
project(L22 LANGUAGES C VERSION 0.1.0)

find_package(L02 CONFIG REQUIRED)
find_package(L03 CONFIG REQUIRED)

add_library(L22 STATIC src/L22.c)
target_include_directories(L22 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L22 PUBLIC L02::L02 L03::L03)

install(TARGETS L22
        EXPORT L22Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L22Targets
        FILE L22Targets.cmake
        NAMESPACE L22::
        DESTINATION lib/cmake/L22)
