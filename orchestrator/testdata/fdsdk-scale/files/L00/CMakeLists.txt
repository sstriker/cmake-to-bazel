cmake_minimum_required(VERSION 3.20)
project(L00 LANGUAGES C VERSION 0.1.0)


add_library(L00 STATIC src/L00.c)
target_include_directories(L00 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L00
        EXPORT L00Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L00Targets
        FILE L00Targets.cmake
        NAMESPACE L00::
        DESTINATION lib/cmake/L00)
