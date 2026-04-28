cmake_minimum_required(VERSION 3.20)
project(L21 LANGUAGES C VERSION 0.1.0)

find_package(L01 CONFIG REQUIRED)
find_package(L02 CONFIG REQUIRED)

add_library(L21 STATIC src/L21.c)
target_include_directories(L21 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L21 PUBLIC L01::L01 L02::L02)

install(TARGETS L21
        EXPORT L21Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L21Targets
        FILE L21Targets.cmake
        NAMESPACE L21::
        DESTINATION lib/cmake/L21)
