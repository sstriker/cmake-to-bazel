cmake_minimum_required(VERSION 3.20)
project(L16 LANGUAGES C VERSION 0.1.0)

find_package(L06 CONFIG REQUIRED)
find_package(L07 CONFIG REQUIRED)

add_library(L16 STATIC src/L16.c)
target_include_directories(L16 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L16 PUBLIC L06::L06 L07::L07)

install(TARGETS L16
        EXPORT L16Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L16Targets
        FILE L16Targets.cmake
        NAMESPACE L16::
        DESTINATION lib/cmake/L16)
