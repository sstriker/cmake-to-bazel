cmake_minimum_required(VERSION 3.20)
project(L09 LANGUAGES C VERSION 0.1.0)


add_library(L09 STATIC src/L09.c)
target_include_directories(L09 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L09
        EXPORT L09Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L09Targets
        FILE L09Targets.cmake
        NAMESPACE L09::
        DESTINATION lib/cmake/L09)
