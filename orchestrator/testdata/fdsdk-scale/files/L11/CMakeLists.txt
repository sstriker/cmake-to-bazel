cmake_minimum_required(VERSION 3.20)
project(L11 LANGUAGES C VERSION 0.1.0)

find_package(L01 CONFIG REQUIRED)
find_package(L02 CONFIG REQUIRED)

add_library(L11 STATIC src/L11.c)
target_include_directories(L11 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L11 PUBLIC L01::L01 L02::L02)

install(TARGETS L11
        EXPORT L11Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L11Targets
        FILE L11Targets.cmake
        NAMESPACE L11::
        DESTINATION lib/cmake/L11)
