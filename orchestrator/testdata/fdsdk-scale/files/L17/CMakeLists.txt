cmake_minimum_required(VERSION 3.20)
project(L17 LANGUAGES C VERSION 0.1.0)

find_package(L07 CONFIG REQUIRED)
find_package(L08 CONFIG REQUIRED)

add_library(L17 STATIC src/L17.c)
target_include_directories(L17 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L17 PUBLIC L07::L07 L08::L08)

install(TARGETS L17
        EXPORT L17Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L17Targets
        FILE L17Targets.cmake
        NAMESPACE L17::
        DESTINATION lib/cmake/L17)
