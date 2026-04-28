cmake_minimum_required(VERSION 3.20)
project(L35 LANGUAGES C VERSION 0.1.0)

find_package(L15 CONFIG REQUIRED)
find_package(L18 CONFIG REQUIRED)

add_library(L35 STATIC src/L35.c)
target_include_directories(L35 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L35 PUBLIC L15::L15 L18::L18)

install(TARGETS L35
        EXPORT L35Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L35Targets
        FILE L35Targets.cmake
        NAMESPACE L35::
        DESTINATION lib/cmake/L35)
