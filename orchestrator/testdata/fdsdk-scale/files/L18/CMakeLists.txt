cmake_minimum_required(VERSION 3.20)
project(L18 LANGUAGES C VERSION 0.1.0)

find_package(L08 CONFIG REQUIRED)
find_package(L09 CONFIG REQUIRED)

add_library(L18 STATIC src/L18.c)
target_include_directories(L18 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L18 PUBLIC L08::L08 L09::L09)

install(TARGETS L18
        EXPORT L18Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L18Targets
        FILE L18Targets.cmake
        NAMESPACE L18::
        DESTINATION lib/cmake/L18)
