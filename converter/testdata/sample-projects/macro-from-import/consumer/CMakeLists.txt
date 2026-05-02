cmake_minimum_required(VERSION 3.20)
project(mfi LANGUAGES C VERSION 0.1.0)

# Consumer of producer-shipped cmake macros. The producer
# element's install-tree `lib/cmake/<Pkg>/` is staged on
# CMAKE_MODULE_PATH (in the meta-project shape, by Bazel
# action-input plumbing; in this fixture's recording, by
# record-extra.sh setting -DCMAKE_MODULE_PATH).
#
# include(Helpers) loads the producer's macros; pkg_link_zlib
# and pkg_add_includes then run target_link_libraries /
# target_include_directories on this consumer's `mfi`
# target. Trace records those calls' `file` field as the
# producer's Helpers.cmake — outside this consumer's source
# root. lower's trace filter has to recover them via the
# consumer-target-name rescue path.
include(Helpers)

find_package(ZLIB REQUIRED)
add_library(mfi STATIC src/mfi.c)

# These two calls live in producer/Helpers.cmake; trace
# records their resolved args here but the `file` field
# points at the producer .cmake module.
pkg_link_zlib(mfi)
pkg_add_includes(mfi)

install(TARGETS mfi
        EXPORT mfiTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT mfiTargets
        FILE mfiTargets.cmake
        NAMESPACE mfi::
        DESTINATION lib/cmake/mfi)
