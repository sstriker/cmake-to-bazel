cmake_minimum_required(VERSION 3.20)
project(L27 LANGUAGES C VERSION 0.1.0)

find_package(L07 CONFIG REQUIRED)
find_package(L08 CONFIG REQUIRED)

add_library(L27 STATIC src/L27.c)
target_include_directories(L27 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L27 PUBLIC L07::L07 L08::L08)

install(TARGETS L27
        EXPORT L27Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L27Targets
        FILE L27Targets.cmake
        NAMESPACE L27::
        DESTINATION lib/cmake/L27)
