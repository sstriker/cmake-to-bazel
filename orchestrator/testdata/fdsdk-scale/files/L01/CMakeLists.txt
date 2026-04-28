cmake_minimum_required(VERSION 3.20)
project(L01 LANGUAGES C VERSION 0.1.0)


add_library(L01 STATIC src/L01.c)
target_include_directories(L01 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L01
        EXPORT L01Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L01Targets
        FILE L01Targets.cmake
        NAMESPACE L01::
        DESTINATION lib/cmake/L01)
