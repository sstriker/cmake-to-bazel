cmake_minimum_required(VERSION 3.20)
project(multilang LANGUAGES C CXX VERSION 0.1.0)

# A library that has both C and C++ translation units. cmake's
# codemodel emits one CompileGroup per language; convert-element's
# current "first compile group only" assumption (per lower's
# `cg := t.CompileGroups[0]`) is exactly the place this fixture
# tests. With C and C++ in one target, the C compile-group's
# flags and includes go in; C++'s drop on the floor.
add_library(multilang STATIC src/c_part.c src/cxx_part.cpp)
target_include_directories(multilang PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

# Per-language flags via the canonical generator-expression form.
target_compile_options(multilang PRIVATE
    $<$<COMPILE_LANGUAGE:C>:-std=c11>
    $<$<COMPILE_LANGUAGE:CXX>:-std=c++17>)

install(TARGETS multilang
        EXPORT multilangTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT multilangTargets
        FILE multilangTargets.cmake
        NAMESPACE multilang::
        DESTINATION lib/cmake/multilang)
