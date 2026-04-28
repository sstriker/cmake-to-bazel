cmake_minimum_required(VERSION 3.20)
project(L04 LANGUAGES C VERSION 0.1.0)


add_library(L04 STATIC src/L04.c)
target_include_directories(L04 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L04
        EXPORT L04Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L04Targets
        FILE L04Targets.cmake
        NAMESPACE L04::
        DESTINATION lib/cmake/L04)
