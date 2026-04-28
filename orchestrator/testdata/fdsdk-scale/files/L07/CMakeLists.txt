cmake_minimum_required(VERSION 3.20)
project(L07 LANGUAGES C VERSION 0.1.0)


add_library(L07 STATIC src/L07.c)
target_include_directories(L07 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L07
        EXPORT L07Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L07Targets
        FILE L07Targets.cmake
        NAMESPACE L07::
        DESTINATION lib/cmake/L07)
