cmake_minimum_required(VERSION 3.20)
project(cmake_consumer LANGUAGES C)

# The M5 CMake-side consumer acceptance gate. Configures against a
# CMAKE_PREFIX_PATH the orchestrator built (its synth-prefix tree),
# resolves a converted element via find_package, and binds the imported
# target to a small downstream library. We don't link/build the
# library — configure-time success is the gate; the IMPORTED_LOCATION
# stubs the orchestrator stages aren't real archives.
find_package(hello CONFIG REQUIRED)

# Touch the imported target to force generation of the link
# interface; if the bundle's targets file is broken, this fails.
get_target_property(_hello_loc hello::hello IMPORTED_LOCATION_RELEASE)
if(NOT _hello_loc)
    message(FATAL_ERROR "find_package(hello) succeeded but hello::hello has no IMPORTED_LOCATION_RELEASE")
endif()
message(STATUS "M5 cmake-consumer found hello::hello at ${_hello_loc}")
