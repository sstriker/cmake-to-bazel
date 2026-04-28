cmake_minimum_required(VERSION 3.20)
project(L42 LANGUAGES C VERSION 0.1.0)

find_package(L22 CONFIG REQUIRED)
find_package(L25 CONFIG REQUIRED)

add_library(L42 STATIC src/L42.c)
target_include_directories(L42 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L42 PUBLIC L22::L22 L25::L25)

install(TARGETS L42
        EXPORT L42Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L42Targets
        FILE L42Targets.cmake
        NAMESPACE L42::
        DESTINATION lib/cmake/L42)
