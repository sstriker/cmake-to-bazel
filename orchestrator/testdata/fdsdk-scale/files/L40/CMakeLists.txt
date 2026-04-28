cmake_minimum_required(VERSION 3.20)
project(L40 LANGUAGES C VERSION 0.1.0)

find_package(L20 CONFIG REQUIRED)
find_package(L23 CONFIG REQUIRED)

add_library(L40 STATIC src/L40.c)
target_include_directories(L40 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L40 PUBLIC L20::L20 L23::L23)

install(TARGETS L40
        EXPORT L40Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L40Targets
        FILE L40Targets.cmake
        NAMESPACE L40::
        DESTINATION lib/cmake/L40)
