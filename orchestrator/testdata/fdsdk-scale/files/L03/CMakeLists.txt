cmake_minimum_required(VERSION 3.20)
project(L03 LANGUAGES C VERSION 0.1.0)


add_library(L03 STATIC src/L03.c)
target_include_directories(L03 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L03
        EXPORT L03Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L03Targets
        FILE L03Targets.cmake
        NAMESPACE L03::
        DESTINATION lib/cmake/L03)
