cmake_minimum_required(VERSION 3.20)
project(L43 LANGUAGES C VERSION 0.1.0)

find_package(L23 CONFIG REQUIRED)
find_package(L26 CONFIG REQUIRED)

add_library(L43 STATIC src/L43.c)
target_include_directories(L43 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L43 PUBLIC L23::L23 L26::L26)

install(TARGETS L43
        EXPORT L43Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L43Targets
        FILE L43Targets.cmake
        NAMESPACE L43::
        DESTINATION lib/cmake/L43)
