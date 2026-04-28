cmake_minimum_required(VERSION 3.20)
project(L23 LANGUAGES C VERSION 0.1.0)

find_package(L03 CONFIG REQUIRED)
find_package(L04 CONFIG REQUIRED)

add_library(L23 STATIC src/L23.c)
target_include_directories(L23 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L23 PUBLIC L03::L03 L04::L04)

install(TARGETS L23
        EXPORT L23Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L23Targets
        FILE L23Targets.cmake
        NAMESPACE L23::
        DESTINATION lib/cmake/L23)
