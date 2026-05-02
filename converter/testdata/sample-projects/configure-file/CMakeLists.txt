cmake_minimum_required(VERSION 3.20)
project(cfglib LANGUAGES C VERSION 1.2.3)

# configure_file generates config.h from config.h.in at cmake-configure
# time. Tests two converter properties:
#  1. The generated config.h appears in the codemodel as a build-tree
#     header — convert-element should recognize it and emit it without
#     trying to resolve a source-tree path that doesn't exist.
#  2. The .h.in template is a source-tree input cmake reads at
#     configure time; the read-paths probe should classify it as
#     real (not a zero-stub candidate).
set(CFGLIB_VERSION_MAJOR 1)
set(CFGLIB_VERSION_MINOR 2)
configure_file(
    ${CMAKE_CURRENT_SOURCE_DIR}/src/config.h.in
    ${CMAKE_CURRENT_BINARY_DIR}/config.h
    @ONLY)

add_library(cfglib STATIC src/cfglib.c)
target_include_directories(cfglib PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/src>
    $<BUILD_INTERFACE:${CMAKE_CURRENT_BINARY_DIR}>
    $<INSTALL_INTERFACE:include>)

install(TARGETS cfglib
        EXPORT cfglibTargets
        ARCHIVE DESTINATION lib)
install(EXPORT cfglibTargets
        FILE cfglibTargets.cmake
        NAMESPACE cfglib::
        DESTINATION lib/cmake/cfglib)
