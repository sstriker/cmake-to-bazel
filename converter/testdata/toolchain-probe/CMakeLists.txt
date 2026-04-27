cmake_minimum_required(VERSION 3.20)

# Trivial two-language probe project used by derive-toolchain. The
# point isn't to build anything useful — it's to make cmake report
# its complete picture of the host toolchain through the File API:
#
#   - toolchains-v1 includes one entry per language declared in
#     project(LANGUAGES ...). Listing C and CXX gives us both.
#   - cache-v2 includes every CMAKE_<LANG>_COMPILER, _FLAGS,
#     _FLAGS_<BUILD_TYPE>, and the binutils-class tool paths
#     (CMAKE_AR, CMAKE_LINKER, CMAKE_STRIP, ...).
#
# The set(... CACHE STRING "" FORCE) lines below export
# CMAKE_HOST_SYSTEM_NAME / _PROCESSOR / CMAKE_SYSTEM_NAME /
# _PROCESSOR into the cache so the File API picks them up. Without
# these forces, cmake keeps them as ordinary (non-cache) variables
# and toolchain.FromReply has to fall back to compiler-target
# parsing.

project(toolchain_probe LANGUAGES C CXX)

set(CMAKE_HOST_SYSTEM_NAME      "${CMAKE_HOST_SYSTEM_NAME}"      CACHE STRING "" FORCE)
set(CMAKE_HOST_SYSTEM_PROCESSOR "${CMAKE_HOST_SYSTEM_PROCESSOR}" CACHE STRING "" FORCE)
set(CMAKE_SYSTEM_NAME           "${CMAKE_SYSTEM_NAME}"           CACHE STRING "" FORCE)
set(CMAKE_SYSTEM_PROCESSOR      "${CMAKE_SYSTEM_PROCESSOR}"      CACHE STRING "" FORCE)

# A single source per language is enough: cmake's File API populates
# toolchains-v1 from project(LANGUAGES ...), and the per-target
# compile fragments in codemodel-v2 are how we cross-check that the
# flags we extract match what cmake actually invokes.
add_library(probe_c STATIC probe.c)
add_library(probe_cxx STATIC probe.cpp)
