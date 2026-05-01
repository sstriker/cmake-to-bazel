cmake_minimum_required(VERSION 3.20)
project(aliaslib LANGUAGES C VERSION 0.1.0)

# add_library(... ALIAS ...) creates an alternate name for an
# existing target. Common pattern: producer libraries ship
# both a plain name (used by in-tree consumers) and a
# namespaced ALIAS name (used by out-of-tree consumers via
# find_package). e.g. `add_library(aliaslib::aliaslib ALIAS
# aliaslib)` mimics the install(EXPORT NAMESPACE) view in
# the source tree.
#
# cmake codemodel records ONLY the underlying real target
# in configurations[].targets[]; the alias is invisible to
# the File API. But the alias target name surfaces in
# target_link_libraries calls' link command fragments.
# Verifies lower's dep resolution doesn't trip over alias
# names — when a consumer does target_link_libraries(t
# aliaslib::aliaslib), cmake resolves the alias before
# emitting the link fragment, so the dep edge points at
# the real target.
add_library(aliaslib STATIC src/aliaslib.c)
target_include_directories(aliaslib PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

# Source-tree alias matching the install(EXPORT) namespaced view.
add_library(aliaslib::aliaslib ALIAS aliaslib)

# Consumer that links via the alias name. cmake resolves
# aliaslib::aliaslib → aliaslib in the codemodel, so the
# link edge surfaces as a normal in-codebase dep.
add_executable(aliaslib_demo src/demo.c)
target_link_libraries(aliaslib_demo PRIVATE aliaslib::aliaslib)

install(TARGETS aliaslib aliaslib_demo
        EXPORT aliaslibTargets
        ARCHIVE DESTINATION lib
        RUNTIME DESTINATION bin)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT aliaslibTargets
        FILE aliaslibTargets.cmake
        NAMESPACE aliaslib::
        DESTINATION lib/cmake/aliaslib)
