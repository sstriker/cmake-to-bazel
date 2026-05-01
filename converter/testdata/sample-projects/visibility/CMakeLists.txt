cmake_minimum_required(VERSION 3.20)
project(visi LANGUAGES C VERSION 0.1.0)

# Three include-dir visibilities cmake distinguishes:
#   PUBLIC    — used by this target AND propagated to consumers
#   PRIVATE   — used by this target only (build-time include path,
#               not exposed)
#   INTERFACE — propagated to consumers only (typical for
#               header-only libraries)
#
# Bazel cc_library has only `includes` (effectively PUBLIC: every
# consumer sees them) and per-source -I flags via copts (private).
# convert-element should:
#   - Keep PUBLIC dirs in `includes` (consumer-visible).
#   - Keep PRIVATE dirs as well in `includes` (Bazel idiom; build-
#     time -I propagates to consumers but cc_library's -I order
#     still respects layout).
#
# In practice the codemodel exposes both PUBLIC + PRIVATE include
# entries on the target's CompileGroups[].Includes — convert-element
# folds them all into Bazel's `includes`. Whether that's
# semantically right is a question this fixture's golden should
# document.
add_library(visi STATIC src/visi.c)
target_include_directories(visi
    PUBLIC
        $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
        $<INSTALL_INTERFACE:include>
    PRIVATE
        ${CMAKE_CURRENT_SOURCE_DIR}/include/private)

install(TARGETS visi
        EXPORT visiTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include
        PATTERN "private" EXCLUDE)
install(EXPORT visiTargets
        FILE visiTargets.cmake
        NAMESPACE visi::
        DESTINATION lib/cmake/visi)
