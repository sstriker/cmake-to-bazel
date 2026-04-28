cmake_minimum_required(VERSION 3.20)
project(L08 LANGUAGES C VERSION 0.1.0)


add_library(L08 STATIC src/L08.c)
target_include_directories(L08 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L08
        EXPORT L08Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L08Targets
        FILE L08Targets.cmake
        NAMESPACE L08::
        DESTINATION lib/cmake/L08)
