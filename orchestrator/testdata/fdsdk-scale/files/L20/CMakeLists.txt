cmake_minimum_required(VERSION 3.20)
project(L20 LANGUAGES C VERSION 0.1.0)

find_package(L00 CONFIG REQUIRED)
find_package(L01 CONFIG REQUIRED)

add_library(L20 STATIC src/L20.c)
target_include_directories(L20 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L20 PUBLIC L00::L00 L01::L01)

install(TARGETS L20
        EXPORT L20Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L20Targets
        FILE L20Targets.cmake
        NAMESPACE L20::
        DESTINATION lib/cmake/L20)
