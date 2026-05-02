cmake_minimum_required(VERSION 3.20)
project(objlib LANGUAGES C VERSION 0.1.0)

# add_library(... OBJECT) compiles the sources to .o files but
# doesn't archive or link them. Consumers reference
# $<TARGET_OBJECTS:objlib_obj> in their srcs to inline the
# objects into a downstream archive/binary. Common in projects
# that want to share compile flags / source sets across
# multiple downstream artifacts without duplicating compiles.
#
# cmake codemodel records OBJECT_LIBRARY targets with
# CompileGroups (so the per-source flags are visible) but
# Link is nil (no link step). NameOnDisk is nil too —
# OBJECT libs have no on-disk artifact name as a single file.
#
# Bazel analog: cc_library with `alwayslink = True` (so the
# objects are always linked into downstream artifacts even if
# nothing references them by symbol). The consumer just
# `deps`-references this cc_library.
add_library(objlib_obj OBJECT src/a.c src/b.c)
target_include_directories(objlib_obj PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

# Consumer that inlines the objects into a static archive.
# Validates the codemodel surfaces the OBJECT lib's compile
# group and the consumer-archive's link statement.
add_library(objlib_archive STATIC src/main.c $<TARGET_OBJECTS:objlib_obj>)
target_link_libraries(objlib_archive PUBLIC objlib_obj)

install(TARGETS objlib_obj objlib_archive
        EXPORT objlibTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT objlibTargets
        FILE objlibTargets.cmake
        NAMESPACE objlib::
        DESTINATION lib/cmake/objlib)
