cmake_minimum_required(VERSION 3.20)
project(genex LANGUAGES C VERSION 0.1.0)

# Generator expressions ($<...>) are cmake-time conditionals that
# resolve at codemodel-emit time. Common forms:
#   $<BUILD_INTERFACE:dir>     — used during this project's build
#   $<INSTALL_INTERFACE:dir>   — used by downstream consumers
#   $<CONFIG:Release>          — boolean for current build config
#   $<COMPILE_LANGUAGE:C>      — language gate
#
# convert-element should resolve these via the codemodel (cmake
# already evaluated them) — the resolved Includes / Copts /
# Defines surface as plain strings in the codemodel, not as
# generator-expression literals.
add_library(genex STATIC src/genex.c)

# BUILD vs INSTALL interface: BUILD wins during the configure cmake
# we just ran; INSTALL would only fire if a downstream consumer
# imported the export. Codemodel records BUILD interface only.
target_include_directories(genex PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

# Per-config compile flag. Codemodel records the resolved flags
# for the current config (Release) — convert-element should NOT
# see -DDEBUG_BUILD here (that's only for Debug config).
target_compile_definitions(genex PRIVATE
    $<$<CONFIG:Debug>:DEBUG_BUILD=1>
    $<$<CONFIG:Release>:RELEASE_BUILD=1>)

# Per-language flag (no-op here since genex is C-only, but the
# expression should still resolve cleanly).
target_compile_options(genex PRIVATE
    $<$<COMPILE_LANGUAGE:C>:-Wall>
    $<$<COMPILE_LANGUAGE:CXX>:-Wno-unused>)

install(TARGETS genex
        EXPORT genexTargets
        ARCHIVE DESTINATION lib)
install(EXPORT genexTargets
        FILE genexTargets.cmake
        NAMESPACE genex::
        DESTINATION lib/cmake/genex)
