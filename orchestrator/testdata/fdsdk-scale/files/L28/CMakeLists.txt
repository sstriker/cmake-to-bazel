cmake_minimum_required(VERSION 3.20)
project(L28 LANGUAGES C VERSION 0.1.0)

find_package(L08 CONFIG REQUIRED)
find_package(L09 CONFIG REQUIRED)

add_library(L28 STATIC src/L28.c)
target_include_directories(L28 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L28 PUBLIC L08::L08 L09::L09)

install(TARGETS L28
        EXPORT L28Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L28Targets
        FILE L28Targets.cmake
        NAMESPACE L28::
        DESTINATION lib/cmake/L28)
