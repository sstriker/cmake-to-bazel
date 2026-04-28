cmake_minimum_required(VERSION 3.20)
project(L02 LANGUAGES C VERSION 0.1.0)


add_library(L02 STATIC src/L02.c)
target_include_directories(L02 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L02
        EXPORT L02Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L02Targets
        FILE L02Targets.cmake
        NAMESPACE L02::
        DESTINATION lib/cmake/L02)
