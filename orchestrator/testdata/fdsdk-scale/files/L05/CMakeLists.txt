cmake_minimum_required(VERSION 3.20)
project(L05 LANGUAGES C VERSION 0.1.0)


add_library(L05 STATIC src/L05.c)
target_include_directories(L05 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L05
        EXPORT L05Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L05Targets
        FILE L05Targets.cmake
        NAMESPACE L05::
        DESTINATION lib/cmake/L05)
