cmake_minimum_required(VERSION 3.20)
project(L12 LANGUAGES C VERSION 0.1.0)

find_package(L02 CONFIG REQUIRED)
find_package(L03 CONFIG REQUIRED)

add_library(L12 STATIC src/L12.c)
target_include_directories(L12 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L12 PUBLIC L02::L02 L03::L03)

install(TARGETS L12
        EXPORT L12Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L12Targets
        FILE L12Targets.cmake
        NAMESPACE L12::
        DESTINATION lib/cmake/L12)
