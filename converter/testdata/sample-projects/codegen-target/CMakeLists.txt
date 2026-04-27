cmake_minimum_required(VERSION 3.20)
project(codegen LANGUAGES C VERSION 0.1.0)

# Codegen via a Python script — exercises driver=python3 tag extraction
# and the build.ninja CUSTOM_COMMAND -> genrule recovery path.
add_custom_command(
    OUTPUT ${CMAKE_CURRENT_BINARY_DIR}/version.h
    COMMAND /usr/bin/python3 ${CMAKE_CURRENT_SOURCE_DIR}/scripts/gen_version.py ${CMAKE_CURRENT_BINARY_DIR}/version.h "0.1.0"
    DEPENDS ${CMAKE_CURRENT_SOURCE_DIR}/scripts/gen_version.py
    VERBATIM
)

add_custom_target(gen_version DEPENDS ${CMAKE_CURRENT_BINARY_DIR}/version.h)

add_library(codegen STATIC src/codegen.c ${CMAKE_CURRENT_BINARY_DIR}/version.h)
add_dependencies(codegen gen_version)
target_include_directories(codegen PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_BINARY_DIR}>
    $<INSTALL_INTERFACE:include>)

install(TARGETS codegen
        EXPORT codegenTargets
        ARCHIVE DESTINATION lib)
install(EXPORT codegenTargets
        FILE codegenTargets.cmake
        NAMESPACE codegen::
        DESTINATION lib/cmake/codegen)
