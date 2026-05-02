# Producer-element-shipped cmake macros consumed by other
# elements. Mimics the pattern used by KDE's ECM, GoogleTest's
# gtest_discover_tests, etc.: a `.cmake` module that defines
# functions which act on consumer-defined targets.
#
# In the meta-project shape this file ships in the producer
# element's install tree at lib/cmake/<Pkg>/Helpers.cmake;
# convert-element stages the producer's `cmake_config`
# filegroup into the consumer's hermetic action so cmake
# configure can include it.
#
# Trace records every target_link_libraries / target_include_
# directories call this function makes; the call's `file`
# field points at this .cmake module, NOT the consumer's
# CMakeLists. lower's trace filter has to recover those calls
# via the consumer-target-name rescue path.

function(pkg_link_zlib target)
    target_link_libraries(${target} PUBLIC ZLIB::ZLIB)
endfunction()

function(pkg_add_includes target)
    target_include_directories(${target} PUBLIC
        $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>)
endfunction()
