cmake_minimum_required(VERSION 3.20)
project(L15 LANGUAGES C VERSION 0.1.0)

find_package(L05 CONFIG REQUIRED)
find_package(L06 CONFIG REQUIRED)

add_library(L15 STATIC src/L15.c)
target_include_directories(L15 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L15 PUBLIC L05::L05 L06::L06)

install(TARGETS L15
        EXPORT L15Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L15Targets
        FILE L15Targets.cmake
        NAMESPACE L15::
        DESTINATION lib/cmake/L15)
