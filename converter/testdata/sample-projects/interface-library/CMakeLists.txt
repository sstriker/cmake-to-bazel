cmake_minimum_required(VERSION 3.20)
project(iflib LANGUAGES CXX VERSION 0.1.0)

# add_library(... INTERFACE) — a header-only library. cmake's
# codemodel records target.type = INTERFACE_LIBRARY; no
# CompileGroups, no compile flags, no link step. Consumers
# get the INTERFACE include directories and INTERFACE link
# libraries via target_link_libraries.
#
# Bazel analog: cc_library with hdrs/includes only and no
# srcs. Linkers don't see header-only libs at link time;
# transitive consumers get the headers in their compile
# search path.
add_library(iflib INTERFACE)
target_include_directories(iflib INTERFACE
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_sources(iflib INTERFACE
    FILE_SET HEADERS
    BASE_DIRS ${CMAKE_CURRENT_SOURCE_DIR}/include
    FILES include/iflib/iflib.hpp)

# The codemodel only emits target objects for buildable targets;
# pure-INTERFACE libraries are visible as targets[] entries
# only when they have a consumer in the same configuration.
# Consumer below forces iflib into the codemodel.
add_executable(iflib_demo src/demo.cpp)
target_link_libraries(iflib_demo PRIVATE iflib)

install(TARGETS iflib iflib_demo
        EXPORT iflibTargets
        RUNTIME DESTINATION bin
        FILE_SET HEADERS DESTINATION include)
install(EXPORT iflibTargets
        FILE iflibTargets.cmake
        NAMESPACE iflib::
        DESTINATION lib/cmake/iflib)
