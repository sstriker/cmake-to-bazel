cmake_minimum_required(VERSION 3.20)
project(uses_hello LANGUAGES C VERSION 0.1.0)

# Step 4 will add `find_package(hello CONFIG REQUIRED)` and a
# target_link_libraries(... hello::hello) line to exercise the
# orchestrator's synth-prefix tree builder. For step 2 the source is
# self-contained so the per-element subprocess loop can iterate the full
# fixture without a cross-element dep that isn't wired yet.
add_library(uses_hello STATIC src/uses_hello.c)
target_include_directories(uses_hello PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS uses_hello
        EXPORT uses_helloTargets
        ARCHIVE DESTINATION lib)
install(EXPORT uses_helloTargets
        FILE uses_helloTargets.cmake
        NAMESPACE uses_hello::
        DESTINATION lib/cmake/uses_hello)
